package sync2

import (
	"github.com/jmoiron/sqlx"
	"os"
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

func TestDevicesAndTokensTables(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	defer db.Close()
	tokens := NewTokensTable(db, "my_secret")

	alice := "@alice:localhost"
	aliceDevice := "alice_phone"
	aliceSecret1 := "mysecret1"
	// TODO: use a fixed time point so the test is deterministic
	aliceToken1FirstSeen := time.Now()

	// Test a single aliceToken
	t.Log("Insert a new token from Alice.")
	aliceToken, err := tokens.Insert(aliceSecret1, alice, aliceDevice, aliceToken1FirstSeen)
	if err != nil {
		t.Fatalf("Failed to Insert token: %s", err)
	}

	t.Log("The returned Token struct should have been populated correctly.")
	assertEqualTokens(t, tokens, aliceToken, aliceSecret1, alice, aliceDevice, aliceToken1FirstSeen)

	t.Log("Reinsert the same aliceToken.")
	reinsertedToken, err := tokens.Insert(aliceSecret1, alice, aliceDevice, aliceToken1FirstSeen)
	if err != nil {
		t.Fatalf("Failed to Insert token: %s", err)
	}

	t.Log("This should yield an equal Token struct.")
	assertEqualTokens(t, tokens, reinsertedToken, aliceSecret1, alice, aliceDevice, aliceToken1FirstSeen)

	t.Log("Try to mark Alice's token as being used after an hour.")
	err = tokens.MaybeUpdateLastSeen(aliceToken, aliceToken1FirstSeen.Add(time.Hour))
	if err != nil {
		t.Fatalf("Failed to update last seen: %s", err)
	}

	t.Log("The token should not be updated in memory, nor in the DB.")
	assertEqualTimes(t, aliceToken.LastSeen, aliceToken1FirstSeen, "Token.LastSeen mismatch")
	fetchedToken, err := tokens.Token(aliceSecret1)
	if err != nil {
		t.Fatalf("Failed to fetch aliceToken: %s", err)
	}
	assertEqualTokens(t, tokens, fetchedToken, aliceSecret1, alice, aliceDevice, aliceToken1FirstSeen)

	t.Log("Try to mark Alice's token as being used after two days.")
	aliceToken1LastSeen := aliceToken1FirstSeen.Add(48 * time.Hour)
	err = tokens.MaybeUpdateLastSeen(aliceToken, aliceToken1LastSeen)
	if err != nil {
		t.Fatalf("Failed to update last seen: %s", err)
	}

	t.Log("The token should now be updated in-memory and in the DB.")
	assertEqualTimes(t, aliceToken.LastSeen, aliceToken1LastSeen, "Token.LastSeen mismatch")
	fetchedToken, err = tokens.Token(aliceSecret1)
	if err != nil {
		t.Fatalf("Failed to fetch aliceToken: %s", err)
	}
	assertEqualTokens(t, tokens, fetchedToken, aliceSecret1, alice, aliceDevice, aliceToken1LastSeen)

	// Test the joins to the devices table
	t.Log("Add a devices row for Alice")
	devices := NewDevicesTable(db)
	err = devices.InsertDevice(alice, aliceDevice)

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

	t.Log("We should see the new since value when the poller refetches alice's aliceToken")
	_, since, err = tokens.GetTokenAndSince(alice, aliceDevice, aliceToken.AccessTokenHash)
	if err != nil {
		t.Fatalf("Failed to GetTokenAndSince: %s", err)
	}
	assertEqual(t, since, sinceValue, "Device.Since mismatch")

	// Test a second token for Alice
	t.Log("Insert a second token for Alice.")
	aliceSecret2 := "mysecret2"
	aliceToken2FirstSeen := aliceToken1LastSeen.Add(time.Minute)
	aliceToken2, err := tokens.Insert(aliceSecret2, alice, aliceDevice, aliceToken2FirstSeen)
	if err != nil {
		t.Fatalf("Failed to Insert token: %s", err)
	}

	t.Log("The returned Token struct should have been populated correctly.")
	assertEqualTokens(t, tokens, aliceToken2, aliceSecret2, alice, aliceDevice, aliceToken2FirstSeen)

	t.Log("We should also see the new since value when the poller fetches alice's second aliceToken")
	_, since, err = tokens.GetTokenAndSince(alice, aliceDevice, aliceToken2.AccessTokenHash)
	if err != nil {
		t.Fatalf("Failed to GetTokenAndSince: %s", err)
	}
	assertEqual(t, since, sinceValue, "Device.Since mismatch")

	// Test bulk fetching a token for each device
	t.Log("Insert a token and device for Bob.")
	bob := "bob"
	bobDevice := "bob_laptop"
	bobSecret := "yes we can"
	bobToken, err := tokens.Insert(bobSecret, bob, bobDevice, time.Time{})
	if err != nil {
		t.Fatalf("Failed to Insert token: %s", err)
	}
	err = devices.InsertDevice(bob, bobDevice)
	if err != nil {
		t.Fatalf("InsertDevice returned error: %s", err)
	}

	t.Log("Insert a device but no token for Chris.")
	chris := "chris"
	chrisDevice := "chris_desktop"
	err = devices.InsertDevice(chris, chrisDevice)
	if err != nil {
		t.Fatalf("InsertDevice returned error: %s", err)
	}

	t.Log("Fetch a token for every device")
	gotTokens, err := tokens.TokenForEachDevice()
	if err != nil {
		t.Fatalf("Failed TokenForEachDevice: %s", err)
	}

	expectAlice := TokenWithSince{Token: aliceToken2, Since: sinceValue}
	expectBob := TokenWithSince{Token: bobToken, Since: ""}
	wantTokens := []*TokenWithSince{&expectAlice, &expectBob}

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

func assertEqualTokens(t *testing.T, table *TokensTable, got *Token, accessToken, userID, deviceID string, lastSeen time.Time) {
	t.Helper()
	assertEqual(t, got.AccessToken, accessToken, "Token.AccessToken mismatch")
	assertEqual(t, got.AccessTokenHash, hashToken(accessToken), "Token.AccessTokenHashed mismatch")
	// We don't care what the encrypted token is here. The fact that we store encrypted values is an
	// implementation detail; the rest of the program doesn't care.
	assertEqual(t, got.UserID, userID, "Token.UserID mismatch")
	assertEqual(t, got.DeviceID, deviceID, "Token.DeviceID mismatch")
	assertEqualTimes(t, got.LastSeen, lastSeen, "Token.LastSeen mismatch")
}

func assertEqual(t *testing.T, got, want, msg string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %s want %s", msg, got, want)
	}
}

func assertEqualTimes(t *testing.T, got, want time.Time, msg string) {
	t.Helper()
	// Postgres stores timestamps with microsecond resolution, so we might lose some
	// precision by storing and fetching a time.Time in/from the DB. Resolution of
	// a second will suffice.
	if !got.Round(time.Second).Equal(want.Round(time.Second)) {
		t.Fatalf("%s: got %v want %v", msg, got, want)
	}
}
