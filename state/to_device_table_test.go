package state

import (
	"bytes"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/gomatrixserverlib"
)

func TestToDeviceTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	table := NewToDeviceTable(db)
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	deviceID := "FOO"
	msgs := []gomatrixserverlib.SendToDeviceEvent{
		{
			Sender:  "alice",
			Type:    "something",
			Content: []byte(`{"foo":"bar"}`),
		},
		{
			Sender:  "bob",
			Type:    "something",
			Content: []byte(`{"foo":"bar2"}`),
		},
	}
	if err = table.InsertMessages(txn, deviceID, msgs); err != nil {
		t.Fatalf("InsertMessages: %s", err)
	}
	gotMsgs, to, err := table.Messages(txn, deviceID, 0)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if to == 0 {
		t.Fatalf("Messages: got to=0")
	}
	if len(gotMsgs) != len(msgs) {
		t.Fatalf("Messages: got %d messages, want %d", len(gotMsgs), len(msgs))
	}
	for i := range msgs {
		if !bytes.Equal(msgs[i].Content, gotMsgs[i].Content) {
			t.Fatalf("Messages: got %+v want %+v", gotMsgs[i], msgs[i])
		}
	}

	// same to= token, no messages
	gotMsgs, to2, err := table.Messages(txn, deviceID, to)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if to2 != to {
		t.Fatalf("Messages: got to=%d want to=%d", to2, to)
	}
	if len(gotMsgs) > 0 {
		t.Fatalf("Messages: got %d messages, want none", len(gotMsgs))
	}

	// different device ID, no messages
	gotMsgs, to, err = table.Messages(txn, "OTHER_DEVICE", 0)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if to != 0 {
		t.Fatalf("Messages: got to=%d want 0", to)
	}
	if len(gotMsgs) > 0 {
		t.Fatalf("Messages: got %d messages, want none", len(gotMsgs))
	}
}
