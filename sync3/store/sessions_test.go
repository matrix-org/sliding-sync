package store

import (
	"reflect"
	"testing"

	"github.com/matrix-org/sync-v3/sync3/streams"
)

func TestSessions(t *testing.T) {
	deviceID := "DEVICE_ID"
	sessions := NewSessions(postgresConnectionString)
	session, err := sessions.NewSession(deviceID)
	if err != nil {
		t.Fatalf("Failed to NewSession: %s", err)
	}
	if session.ID == 0 {
		t.Fatalf("Session.ID was not set from NewSession")
	}
	assertEqual(t, session.DeviceID, deviceID, "Session.DeviceID mismatch")
	if err = sessions.UpdateDeviceSince(deviceID, "s1"); err != nil {
		t.Fatalf("UpdateDeviceSince returned error: %s", err)
	}
	if err = sessions.UpdateLastTokens(session.ID, "conf", "unconf"); err != nil {
		t.Fatalf("UpdateLastTokens returned error: %s", err)
	}
	if err = sessions.UpdateUserIDForDevice(deviceID, "@alice:localhost"); err != nil {
		t.Fatalf("UpdateUserIDForDevice returned error: %s", err)
	}

	// now check that session retrieval has the latest values
	session, err = sessions.Session(session.ID, deviceID)
	if err != nil {
		t.Fatalf("Session returned error: %s", err)
	}
	assertEqual(t, session.DeviceID, deviceID, "Session.DeviceID mismatch")
	assertEqual(t, session.LastConfirmedToken, "conf", "Session.LastConfirmedToken mismatch")
	assertEqual(t, session.LastUnconfirmedToken, "unconf", "Session.LastUnconfirmedToken mismatch")
	assertEqual(t, session.V2Since, "s1", "Session.V2Since mismatch")
	assertEqual(t, session.UserID, "@alice:localhost", "Session.UserID mismatch")

	// now check new sessions remember the v2 since value and user ID for this device
	s2, err := sessions.NewSession(deviceID)
	if err != nil {
		t.Fatalf("NewSession returned error: %s", err)
	}
	assertEqual(t, s2.V2Since, "s1", "Session.V2Since mismatch")
	assertEqual(t, s2.UserID, "@alice:localhost", "Session.UserID mismatch")

	// check filters work
	wantReq := &streams.Request{
		Typing: &streams.FilterTyping{
			RoomID: "!foo:bar",
		},
	}
	fid, err := sessions.InsertFilter(session.ID, wantReq)
	if err != nil {
		t.Fatalf("InsertFilter failed: %s", err)
	}
	if fid == 0 {
		t.Fatalf("wanted non-zero filter ID, got %d", fid)
	}
	f, err := sessions.Filter(session.ID, fid)
	if err != nil {
		t.Fatalf("Filter returned an error: %s", err)
	}
	if f.Typing.RoomID != wantReq.Typing.RoomID {
		t.Fatalf("Returned filter is corrupt, bad room ID: got %v want %v", f.Typing.RoomID, wantReq.Typing.RoomID)
	}

	// check tokens work
	if err = sessions.UpdateLastTokens(s2.ID, "conf2", "unconf2"); err != nil {
		t.Fatalf("UpdateLastTokens returned error: %s", err)
	}
	tokens, err := sessions.ConfirmedSessionTokens(s2.DeviceID)
	if err != nil {
		t.Fatalf("ConfirmedSessionTokens: %s", err)
	}
	wantTokens := []string{"conf", "conf2"}
	if !reflect.DeepEqual(tokens, wantTokens) {
		t.Fatalf("ConfirmedSessionTokens: got %v want %v", tokens, wantTokens)
	}
}

func assertEqual(t *testing.T, got, want, msg string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %s want %s", msg, got, want)
	}
}
