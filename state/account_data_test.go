package state

import (
	"bytes"
	"sort"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sync2"
)

func assertAccountDatasEqual(t *testing.T, msg string, gots, wants []AccountData) {
	t.Helper()
	key := func(a AccountData) string {
		return a.UserID + a.RoomID + a.Type
	}
	sort.Slice(gots, func(i, j int) bool {
		return key(gots[i]) < key(gots[j])
	})
	sort.Slice(wants, func(i, j int) bool {
		return key(wants[i]) < key(wants[j])
	})
	if len(gots) != len(wants) {
		t.Fatalf("%s: got %v want %v", msg, gots, wants)
	}
	for i := range wants {
		if gots[i].RoomID != wants[i].RoomID {
			t.Errorf("%s[%d]: got room id %v want %v", msg, i, gots[i].RoomID, wants[i].RoomID)
		}
		if gots[i].Type != wants[i].Type {
			t.Errorf("%s[%d]: got type %v want %v", msg, i, gots[i].Type, wants[i].Type)
		}
		if gots[i].UserID != wants[i].UserID {
			t.Errorf("%s[%d]: got user id %v want %v", msg, i, gots[i].UserID, wants[i].UserID)
		}
		if !bytes.Equal(gots[i].Data, wants[i].Data) {
			t.Errorf("%s[%d]: got data %v want %v", msg, i, string(gots[i].Data), string(wants[i].Data))
		}
		if wants[i].ID > 0 && gots[i].ID != wants[i].ID {
			t.Errorf("%s[%d]: got id %v want %v", msg, i, gots[i].ID, wants[i].ID)
		}
	}
}

func TestAccountData(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	alice := "@alice_TestAccountData:localhost"
	roomA := "!TestAccountData_A:localhost"
	roomB := "!TestAccountData_B:localhost"
	eventType := "the_event_type"
	table := NewAccountDataTable(db)
	accountData := []AccountData{
		{
			UserID: alice,
			RoomID: roomA,
			Type:   eventType,
			Data:   []byte(`{"foo":"bar"}`),
		},
		{
			UserID: alice,
			RoomID: roomB,
			Type:   eventType,
			Data:   []byte(`{"foo":"bar2"}`),
		},
		{
			UserID: alice,
			RoomID: roomB,
			Type:   "dummy",
			Data:   []byte(`{"foo":"bar3"}`),
		},
		{
			UserID: "@not_alice:localhost",
			RoomID: roomA,
			Type:   "dummy",
			Data:   []byte(`{"foo":"bar4"}`),
		},
		{
			UserID: alice,
			RoomID: sync2.AccountDataGlobalRoom,
			Type:   eventType,
			Data:   []byte(`{"foo":"bar4"}`),
		},
		{
			UserID: alice,
			RoomID: sync2.AccountDataGlobalRoom,
			Type:   "dummy",
			Data:   []byte(`{"foo":"bar5"}`),
		},
		// this should replace the first element
		{
			UserID: alice,
			RoomID: roomA,
			Type:   eventType,
			Data:   []byte(`{"updated":true}`),
		},
	}
	deduped, err := table.Insert(txn, accountData)
	if err != nil {
		t.Fatalf("Insert: %s", err)
	}
	if len(deduped) != len(accountData)-1 {
		t.Fatalf("Insert: did not dedupe events, got %d events want %d", len(deduped), len(accountData)-1)
	}

	// select the updated event
	gotData, err := table.Select(txn, alice, []string{eventType}, roomA)
	if err != nil {
		t.Fatalf("Select: %s", err)
	}
	assertAccountDatasEqual(t, "Select: expected updated event to be returned but wasn't", []AccountData{gotData[0]}, []AccountData{accountData[len(accountData)-1]})

	// Select the global event
	gotData, err = table.Select(txn, alice, []string{eventType}, sync2.AccountDataGlobalRoom)
	if err != nil {
		t.Fatalf("Select: %s", err)
	}
	assertAccountDatasEqual(t, "Select: expected global event to be returned but wasn't", []AccountData{gotData[0]}, []AccountData{accountData[len(accountData)-3]})

	// Select all global events for alice
	wantDatas := []AccountData{
		accountData[4], accountData[5],
	}
	gotDatas, err := table.SelectMany(txn, alice)
	if err != nil {
		t.Fatalf("SelectMany: %s", err)
	}
	assertAccountDatasEqual(t, "SelectMany", gotDatas, wantDatas)

	// Select all room events for alice
	wantDatas = []AccountData{
		accountData[6],
	}
	gotDatas, err = table.SelectMany(txn, alice, roomA)
	if err != nil {
		t.Fatalf("SelectMany: %s", err)
	}
	assertAccountDatasEqual(t, "SelectMany", gotDatas, wantDatas)

	// Select all room events for unknown user
	gotDatas, err = table.SelectMany(txn, "@someone-else:localhost", roomA)
	if err != nil {
		t.Fatalf("SelectMany: %s", err)
	}
	if len(gotDatas) != 0 {
		t.Fatalf("SelectMany: got %d account data, want 0", len(gotDatas))
	}

	// Select all room account data matching eventType
	gotDatas, err = table.SelectWithType(txn, alice, eventType)
	if err != nil {
		t.Fatalf("SelectWithType: %v", err)
	}
	wantDatas = []AccountData{
		accountData[1], accountData[6],
	}
	assertAccountDatasEqual(t, "SelectWithType", gotDatas, wantDatas)

	// Select all types in this room
	gotDatas, err = table.Select(txn, alice, []string{eventType, "dummy"}, roomB)
	if err != nil {
		t.Fatalf("SelectWithType: %v", err)
	}
	wantDatas = []AccountData{
		accountData[1], accountData[2],
	}
	assertAccountDatasEqual(t, "Select(multi-types)", gotDatas, wantDatas)

}

func TestAccountDataIDIncrements(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	alice := "@alice_TestAccountDataIDIncrements:localhost"
	roomA := "!TestAccountData_A:localhost"
	//roomB := "!TestAccountData_B:localhost"
	eventType := "the_event_type"
	data := AccountData{
		UserID: alice,
		RoomID: roomA,
		Type:   eventType,
		Data:   []byte(`{"foo":"bar"}`),
	}
	table := NewAccountDataTable(db)
	_, err = table.Insert(txn, []AccountData{
		data,
	})
	assertNoError(t, err)
	// make sure all selects return an id
	gots, err := table.SelectWithType(txn, alice, eventType)
	assertNoError(t, err)
	assertAccountDatasEqual(t, "SelectWithType", gots, []AccountData{data})
	if gots[0].ID == 0 {
		t.Fatalf("missing id field")
	}
	data.ID = gots[0].ID
	gots, err = table.Select(txn, alice, []string{eventType}, roomA)
	assertNoError(t, err)
	assertAccountDatasEqual(t, "Select", gots, []AccountData{data})
	gots, err = table.SelectMany(txn, alice, roomA)
	assertNoError(t, err)
	assertAccountDatasEqual(t, "SelectMany", gots, []AccountData{data})
	// now replace the data, which should update the id
	data.Data = []byte(`{"foo":"bar2"}`)
	_, err = table.Insert(txn, []AccountData{
		data,
	})
	assertNoError(t, err)
	gots, err = table.Select(txn, alice, []string{eventType}, roomA)
	assertNoError(t, err)
	if gots[0].ID < data.ID {
		t.Fatalf("id was not incremented, got %d want %d", gots[0].ID, data.ID)
	}
	data.ID = gots[0].ID
	assertAccountDatasEqual(t, "Select", gots, []AccountData{data})
	gots, err = table.SelectMany(txn, alice, roomA)
	assertNoError(t, err)
	assertAccountDatasEqual(t, "SelectMany", gots, []AccountData{data})
	gots, err = table.SelectWithType(txn, alice, eventType)
	assertNoError(t, err)
	assertAccountDatasEqual(t, "SelectWithType", gots, []AccountData{data})
}
