package state

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestToDeviceTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	table := NewToDeviceTable(db)
	deviceID := "FOO"
	var limit int64 = 999
	msgs := []json.RawMessage{
		json.RawMessage(`{"sender":"alice","type":"something","content":{"foo":"bar"}}`),
		json.RawMessage(`{"sender":"bob","type":"something","content":{"foo":"bar2"}}`),
	}
	var lastPos int64
	if lastPos, err = table.InsertMessages(deviceID, msgs); err != nil {
		t.Fatalf("InsertMessages: %s", err)
	}
	if lastPos != 2 {
		t.Fatalf("InsertMessages: bad pos returned, got %d want 2", lastPos)
	}
	gotMsgs, upTo, err := table.Messages(deviceID, 0, lastPos, limit)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if upTo != lastPos {
		t.Errorf("Message: got up to %d want %d", upTo, lastPos)
	}
	if len(gotMsgs) != len(msgs) {
		t.Fatalf("Messages: got %d messages, want %d", len(gotMsgs), len(msgs))
	}
	for i := range msgs {
		if !bytes.Equal(msgs[i], gotMsgs[i]) {
			t.Fatalf("Messages: got %+v want %+v", gotMsgs[i], msgs[i])
		}
	}
	// -1 to value means latest position
	gotMsgs, upTo, err = table.Messages(deviceID, 0, -1, limit)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if upTo != lastPos {
		t.Errorf("Message: got up to %d want %d", upTo, lastPos)
	}
	if len(gotMsgs) != len(msgs) {
		t.Fatalf("Messages: got %d messages, want %d", len(gotMsgs), len(msgs))
	}
	for i := range msgs {
		if !bytes.Equal(msgs[i], gotMsgs[i]) {
			t.Fatalf("Messages: got %+v want %+v", gotMsgs[i], msgs[i])
		}
	}

	// same to= token, no messages
	gotMsgs, upTo, err = table.Messages(deviceID, lastPos, lastPos, limit)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if upTo != lastPos {
		t.Errorf("Message: got up to %d want %d", upTo, lastPos)
	}
	if len(gotMsgs) > 0 {
		t.Fatalf("Messages: got %d messages, want none", len(gotMsgs))
	}

	// different device ID, no messages
	gotMsgs, upTo, err = table.Messages("OTHER_DEVICE", 0, lastPos, limit)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if upTo != lastPos {
		t.Errorf("Message: got up to %d want %d", upTo, lastPos)
	}
	if len(gotMsgs) > 0 {
		t.Fatalf("Messages: got %d messages, want none", len(gotMsgs))
	}

	// zero limit, no messages
	gotMsgs, upTo, err = table.Messages(deviceID, 0, lastPos, 0)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if upTo != lastPos {
		t.Errorf("Message: got up to %d want %d", upTo, lastPos)
	}
	if len(gotMsgs) > 0 {
		t.Fatalf("Messages: got %d messages, want none", len(gotMsgs))
	}

	// lower limit, cap out
	var wantLimit int64 = 1
	gotMsgs, upTo, err = table.Messages(deviceID, 0, lastPos, wantLimit)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	// we inserted 2 messages, and request a limit of 1 so the position should be one before
	if upTo != (lastPos - 1) {
		t.Errorf("Message: got up to %d want %d", upTo, lastPos-1)
	}
	if int64(len(gotMsgs)) != wantLimit {
		t.Fatalf("Messages: got %d messages, want %d", len(gotMsgs), wantLimit)
	}

	// delete the first message, requerying only gives 1 message
	if err := table.DeleteMessagesUpToAndIncluding(deviceID, lastPos-1); err != nil {
		t.Fatalf("DeleteMessagesUpTo: %s", err)
	}
	gotMsgs, upTo, err = table.Messages(deviceID, 0, lastPos, limit)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if upTo != lastPos {
		t.Errorf("Message: got up to %d want %d", upTo, lastPos)
	}
	if len(gotMsgs) != 1 {
		t.Fatalf("Messages: got %d messages, want %d", len(gotMsgs), 1)
	}
	want, err := json.Marshal(msgs[1])
	if err != nil {
		t.Fatalf("failed to marshal msg: %s", err)
	}
	if !bytes.Equal(gotMsgs[0], want) {
		t.Fatalf("Messages: deleted message but unexpected message left: got %s want %s", string(gotMsgs[0]), string(want))
	}
}
