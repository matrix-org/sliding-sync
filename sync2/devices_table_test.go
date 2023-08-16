package sync2

import (
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/testutils"
)

var postgresConnectionString = "user=xxxxx dbname=syncv3_test sslmode=disable"

func TestMain(m *testing.M) {
	postgresConnectionString = testutils.PrepareDBConnectionString()
	exitCode := m.Run()
	os.Exit(exitCode)
}

func connectToDB(t *testing.T) (*sqlx.DB, func()) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	return db, func() {
		db.Close()
	}
}

// Note that we currently only ever read from (devices JOIN tokens), so there is some
// overlap with tokens_table_test.go here.
func TestDevicesTableSinceColumn(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	tokens := NewTokensTable(db, "my_secret")
	devices := NewDevicesTable(db)

	alice := "@alice:localhost"
	aliceDevice := "alice_phone"
	aliceSecret1 := "mysecret1"
	aliceSecret2 := "mysecret2"

	var aliceToken, aliceToken2 *Token
	_ = sqlutil.WithTransaction(db, func(txn *sqlx.Tx) (err error) {
		t.Log("Insert two tokens for Alice.")
		aliceToken, err = tokens.Insert(txn, aliceSecret1, alice, aliceDevice, time.Now())
		if err != nil {
			t.Fatalf("Failed to Insert token: %s", err)
		}
		aliceToken2, err = tokens.Insert(txn, aliceSecret2, alice, aliceDevice, time.Now())
		if err != nil {
			t.Fatalf("Failed to Insert token: %s", err)
		}

		t.Log("Add a devices row for Alice")
		err = devices.InsertDevice(txn, alice, aliceDevice)
		if err != nil {
			t.Fatalf("Failed to Insert device: %s", err)
		}
		return nil
	})

	t.Log("Pretend we're about to start a poller. Fetch Alice's token along with the since value tracked by the devices table.")
	accessToken, since, err := tokens.GetTokenAndSince(alice, aliceDevice, aliceToken.AccessTokenHash)
	if err != nil {
		t.Fatalf("Failed to GetTokenAndSince: %s", err)
	}

	t.Log("The since token should be empty.")
	assertEqual(t, accessToken, aliceToken.AccessToken, "Token.AccessToken mismatch")
	assertEqual(t, since, "", "Device.Since mismatch")

	t.Log("Update the since column.")
	sinceValue := "s-1-2-3-4"
	err = devices.UpdateDeviceSince(alice, aliceDevice, sinceValue)
	if err != nil {
		t.Fatalf("Failed to update since column: %s", err)
	}

	t.Log("We should see the new since value when the poller refetches alice's token")
	_, since, err = tokens.GetTokenAndSince(alice, aliceDevice, aliceToken.AccessTokenHash)
	if err != nil {
		t.Fatalf("Failed to GetTokenAndSince: %s", err)
	}
	assertEqual(t, since, sinceValue, "Device.Since mismatch")

	t.Log("We should also see the new since value when the poller fetches alice's second token")
	_, since, err = tokens.GetTokenAndSince(alice, aliceDevice, aliceToken2.AccessTokenHash)
	if err != nil {
		t.Fatalf("Failed to GetTokenAndSince: %s", err)
	}
	assertEqual(t, since, sinceValue, "Device.Since mismatch")
}

func TestTokenForEachDevice(t *testing.T) {
	db, close := connectToDB(t)
	defer close()

	// HACK: discard rows inserted by other tests. We don't normally need to do this,
	// but this is testing a query that scans the entire devices table.
	db.Exec("TRUNCATE syncv3_sync2_devices, syncv3_sync2_tokens;")

	tokens := NewTokensTable(db, "my_secret")
	devices := NewDevicesTable(db)

	alice := "alice"
	aliceDevice := "alice_phone"
	bob := "bob"
	bobDevice := "bob_laptop"
	chris := "chris"
	chrisDevice := "chris_desktop"

	_ = sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		t.Log("Add a device for Alice, Bob and Chris.")
		err := devices.InsertDevice(txn, alice, aliceDevice)
		if err != nil {
			t.Fatalf("InsertDevice returned error: %s", err)
		}
		err = devices.InsertDevice(txn, bob, bobDevice)
		if err != nil {
			t.Fatalf("InsertDevice returned error: %s", err)
		}
		err = devices.InsertDevice(txn, chris, chrisDevice)
		if err != nil {
			t.Fatalf("InsertDevice returned error: %s", err)
		}
		return nil
	})

	t.Log("Mark Alice's device with a since token.")
	sinceValue := "s-1-2-3-4"
	err := devices.UpdateDeviceSince(alice, aliceDevice, sinceValue)
	if err != nil {
		t.Fatalf("UpdateDeviceSince returned error: %s", err)
	}

	var aliceToken2, bobToken *Token
	_ = sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		t.Log("Insert 2 tokens for Alice, one for Bob and none for Chris.")
		aliceLastSeen1 := time.Now()
		_, err = tokens.Insert(txn, "alice_secret", alice, aliceDevice, aliceLastSeen1)
		if err != nil {
			t.Fatalf("Failed to Insert token: %s", err)
		}
		aliceLastSeen2 := aliceLastSeen1.Add(1 * time.Minute)
		aliceToken2, err = tokens.Insert(txn, "alice_secret2", alice, aliceDevice, aliceLastSeen2)
		if err != nil {
			t.Fatalf("Failed to Insert token: %s", err)
		}
		bobToken, err = tokens.Insert(txn, "bob_secret", bob, bobDevice, time.Time{})
		if err != nil {
			t.Fatalf("Failed to Insert token: %s", err)
		}
		return nil
	})

	t.Log("Fetch a token for every device")
	gotTokens, err := tokens.TokenForEachDevice(nil)
	if err != nil {
		t.Fatalf("Failed TokenForEachDevice: %s", err)
	}

	expectAlice := TokenForPoller{Token: aliceToken2, Since: sinceValue}
	expectBob := TokenForPoller{Token: bobToken, Since: ""}
	wantTokens := []*TokenForPoller{&expectAlice, &expectBob}

	if len(gotTokens) != len(wantTokens) {
		t.Fatalf("AllDevices: got %d tokens, want %d", len(gotTokens), len(wantTokens))
	}

	sort.Slice(gotTokens, func(i, j int) bool {
		if gotTokens[i].UserID < gotTokens[j].UserID {
			return true
		}
		return gotTokens[i].DeviceID < gotTokens[j].DeviceID
	})

	for i := range gotTokens {
		assertEqual(t, gotTokens[i].Since, wantTokens[i].Since, "Device.Since mismatch")
		assertEqual(t, gotTokens[i].UserID, wantTokens[i].UserID, "Token.UserID mismatch")
		assertEqual(t, gotTokens[i].DeviceID, wantTokens[i].DeviceID, "Token.DeviceID mismatch")
		assertEqual(t, gotTokens[i].AccessToken, wantTokens[i].AccessToken, "Token.AccessToken mismatch")
	}
}

func TestDevicesTable_FindOldDevices(t *testing.T) {
	db, close := connectToDB(t)
	defer close()

	// HACK: discard rows inserted by other tests. We don't normally need to do this,
	// but this is testing a query that scans the entire devices table.
	db.Exec("TRUNCATE syncv3_sync2_devices, syncv3_sync2_tokens;")

	tokens := NewTokensTable(db, "my_secret")
	devices := NewDevicesTable(db)

	tcs := []struct {
		UserID    string
		DeviceID  string
		tokenAges []time.Duration
	}{
		{UserID: "@alice:test", DeviceID: "no_tokens", tokenAges: nil},
		{UserID: "@bob:test", DeviceID: "one_active_token", tokenAges: []time.Duration{time.Hour}},
		{UserID: "@bob:test", DeviceID: "one_old_token", tokenAges: []time.Duration{7 * 24 * time.Hour}},
		{UserID: "@chris:test", DeviceID: "one_old_one_active", tokenAges: []time.Duration{time.Hour, 7 * 24 * time.Hour}},
		{UserID: "@delia:test", DeviceID: "two_old_tokens", tokenAges: []time.Duration{7 * 24 * time.Hour, 14 * 24 * time.Hour}},
	}

	txn, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	numTokens := 0
	for _, tc := range tcs {
		err = devices.InsertDevice(txn, tc.UserID, tc.DeviceID)
		if err != nil {
			t.Fatal(err)
		}
		for _, age := range tc.tokenAges {
			numTokens++
			_, err = tokens.Insert(
				txn,
				fmt.Sprintf("token-%d", numTokens),
				tc.UserID,
				tc.DeviceID,
				time.Now().Add(-age),
			)
		}
	}
	err = txn.Commit()
	if err != nil {
		t.Fatal(err)
	}

	oldDevices, err := devices.FindOldDevices(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(oldDevices, func(i, j int) bool {
		return oldDevices[i].UserID < oldDevices[j].UserID
	})
	expectedDevices := []Device{
		{UserID: "@bob:test", DeviceID: "one_old_token"},
		{UserID: "@delia:test", DeviceID: "two_old_tokens"},
	}

	if !reflect.DeepEqual(oldDevices, expectedDevices) {
		t.Errorf("Got %+v, but expected %v+", oldDevices, expectedDevices)
	}
}
