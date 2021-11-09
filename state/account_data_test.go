package state

import (
	"reflect"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sync-v3/sync2"
)

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
	gotData, err := table.Select(txn, alice, eventType, roomA)
	if err != nil {
		t.Fatalf("Select: %s", err)
	}
	if !reflect.DeepEqual(*gotData, accountData[len(accountData)-1]) {
		t.Fatalf("Select: expected updated event to be returned but wasn't. Got %+v want %+v", gotData, accountData[len(accountData)-1])
	}

	// Select the global event
	gotData, err = table.Select(txn, alice, eventType, sync2.AccountDataGlobalRoom)
	if err != nil {
		t.Fatalf("Select: %s", err)
	}
	if !reflect.DeepEqual(*gotData, accountData[len(accountData)-2]) {
		t.Fatalf("Select: expected global event to be returned but wasn't. Got %+v want %+v", gotData, accountData[len(accountData)-2])
	}

}
