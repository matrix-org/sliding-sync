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
	gotMsgs, upTo, err := table.Messages(deviceID, 0, limit)
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
	gotMsgs, upTo, err = table.Messages(deviceID, lastPos, limit)
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
	gotMsgs, upTo, err = table.Messages("OTHER_DEVICE", 0, limit)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if upTo != 0 {
		t.Errorf("Message: got up to %d want %d", upTo, 0)
	}
	if len(gotMsgs) > 0 {
		t.Fatalf("Messages: got %d messages, want none", len(gotMsgs))
	}

	// zero limit, no messages
	gotMsgs, upTo, err = table.Messages(deviceID, 0, 0)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if upTo != 0 {
		t.Errorf("Message: got up to %d want %d", upTo, 0)
	}
	if len(gotMsgs) > 0 {
		t.Fatalf("Messages: got %d messages, want none", len(gotMsgs))
	}

	// lower limit, cap out
	var wantLimit int64 = 1
	gotMsgs, upTo, err = table.Messages(deviceID, 0, wantLimit)
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
	gotMsgs, upTo, err = table.Messages(deviceID, 0, limit)
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

// Test that https://github.com/uhoreg/matrix-doc/blob/drop-stale-to-device/proposals/3944-drop-stale-to-device.md works for m.room_key_request
func TestToDeviceTableDeleteCancels(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	sender := "SENDER"
	destination := "DEST"
	table := NewToDeviceTable(db)
	// insert 2 requests
	reqEv1 := newRoomKeyEvent(t, "request", "1", sender, map[string]interface{}{
		"foo": "bar",
	})
	_, err = table.InsertMessages(destination, []json.RawMessage{reqEv1})
	assertNoError(t, err)
	gotMsgs, _, err := table.Messages(destination, 0, 10)
	assertNoError(t, err)
	bytesEqual(t, gotMsgs[0], reqEv1)
	reqEv2 := newRoomKeyEvent(t, "request", "2", sender, map[string]interface{}{
		"foo": "baz",
	})
	_, err = table.InsertMessages(destination, []json.RawMessage{reqEv2})
	assertNoError(t, err)
	gotMsgs, _, err = table.Messages(destination, 0, 10)
	assertNoError(t, err)
	bytesEqual(t, gotMsgs[1], reqEv2)

	// now delete 1
	cancelEv1 := newRoomKeyEvent(t, "request_cancellation", "1", sender, nil)
	_, err = table.InsertMessages(destination, []json.RawMessage{cancelEv1})
	assertNoError(t, err)
	// selecting messages now returns only reqEv2
	gotMsgs, _, err = table.Messages(destination, 0, 10)
	assertNoError(t, err)
	bytesEqual(t, gotMsgs[0], reqEv2)

	// now do lots of close but not quite cancellation requests that should not match reqEv2
	_, err = table.InsertMessages(destination, []json.RawMessage{
		newRoomKeyEvent(t, "cancellation", "2", sender, nil),                      // wrong action
		newRoomKeyEvent(t, "request_cancellation", "22", sender, nil),             // wrong request ID
		newRoomKeyEvent(t, "request_cancellation", "2", "not_who_you_think", nil), // wrong req device id
	})
	assertNoError(t, err)
	_, err = table.InsertMessages("wrong_destination", []json.RawMessage{ // wrong destination
		newRoomKeyEvent(t, "request_cancellation", "2", sender, nil),
	})
	assertNoError(t, err)
	gotMsgs, _, err = table.Messages(destination, 0, 10)
	assertNoError(t, err)
	bytesEqual(t, gotMsgs[0], reqEv2) // the request lives on
	if len(gotMsgs) != 4 {            // the cancellations live on too, but not the one sent to the wrong dest
		t.Errorf("got %d msgs, want 4", len(gotMsgs))
	}

	// request + cancel in one go => nothing inserted
	destination2 := "DEST2"
	_, err = table.InsertMessages(destination2, []json.RawMessage{
		newRoomKeyEvent(t, "request", "A", sender, map[string]interface{}{
			"foo": "baz",
		}),
		newRoomKeyEvent(t, "request_cancellation", "A", sender, nil),
	})
	assertNoError(t, err)
	gotMsgs, _, err = table.Messages(destination2, 0, 10)
	assertNoError(t, err)
	if len(gotMsgs) > 0 {
		t.Errorf("Got %+v want nothing", jsonArrStr(gotMsgs))
	}
}

// Test that unacked events are safe from deletion
func TestToDeviceTableNoDeleteUnacks(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	sender := "SENDER2"
	destination := "DEST2"
	table := NewToDeviceTable(db)
	// insert request
	reqEv := newRoomKeyEvent(t, "request", "1", sender, map[string]interface{}{
		"foo": "bar",
	})
	pos, err := table.InsertMessages(destination, []json.RawMessage{reqEv})
	assertNoError(t, err)
	// mark this position as unacked: this means the client MAY know about this request so it isn't
	// safe to delete it
	err = table.SetUnackedPosition(destination, pos)
	assertNoError(t, err)
	// now issue a cancellation: this should NOT result in a cancellation due to protection for unacked events
	cancelEv := newRoomKeyEvent(t, "request_cancellation", "1", sender, nil)
	_, err = table.InsertMessages(destination, []json.RawMessage{cancelEv})
	assertNoError(t, err)
	// selecting messages returns both events
	gotMsgs, _, err := table.Messages(destination, 0, 10)
	assertNoError(t, err)
	if len(gotMsgs) != 2 {
		t.Fatalf("got %d msgs, want 2: %v", len(gotMsgs), jsonArrStr(gotMsgs))
	}
	bytesEqual(t, gotMsgs[0], reqEv)
	bytesEqual(t, gotMsgs[1], cancelEv)

	// test that injecting another req/cancel does cause them to be deleted
	_, err = table.InsertMessages(destination, []json.RawMessage{newRoomKeyEvent(t, "request", "2", sender, map[string]interface{}{
		"foo": "bar",
	})})
	assertNoError(t, err)
	_, err = table.InsertMessages(destination, []json.RawMessage{newRoomKeyEvent(t, "request_cancellation", "2", sender, nil)})
	assertNoError(t, err)
	// selecting messages returns the same as before
	gotMsgs, _, err = table.Messages(destination, 0, 10)
	assertNoError(t, err)
	if len(gotMsgs) != 2 {
		t.Fatalf("got %d msgs, want 2: %v", len(gotMsgs), jsonArrStr(gotMsgs))
	}
	bytesEqual(t, gotMsgs[0], reqEv)
	bytesEqual(t, gotMsgs[1], cancelEv)
}

// Guard against possible message truncation?
func TestToDeviceTableBytesInEqualBytesOut(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	table := NewToDeviceTable(db)
	testCases := []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"foo":"bar"}`),
		json.RawMessage(`{  "foo":   "bar" }`),
		json.RawMessage(`{ not even valid json :D }`),
		json.RawMessage(`{ "\~./.-$%_!@£?;'\[]= }`),
	}
	var pos int64
	for _, msg := range testCases {
		nextPos, err := table.InsertMessages("A", []json.RawMessage{msg})
		if err != nil {
			t.Fatalf("InsertMessages: %s", err)
		}
		got, _, err := table.Messages("A", pos, 1)
		if err != nil {
			t.Fatalf("Messages: %s", err)
		}
		bytesEqual(t, got[0], msg)
		pos = nextPos
	}
	// and all at once
	_, err = table.InsertMessages("B", testCases)
	if err != nil {
		t.Fatalf("InsertMessages: %s", err)
	}
	got, _, err := table.Messages("B", 0, 100)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if len(got) != len(testCases) {
		t.Fatalf("got %d messages, want %d", len(got), len(testCases))
	}
	for i := range testCases {
		bytesEqual(t, got[i], testCases[i])
	}
}

func bytesEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("bytesEqual: \ngot  %s\n want %s", string(got), string(want))
	}
}

type roomKeyRequest struct {
	Type    string                `json:"type"`
	Content roomKeyRequestContent `json:"content"`
}

type roomKeyRequestContent struct {
	Action             string                 `json:"action"`
	RequestID          string                 `json:"request_id"`
	RequestingDeviceID string                 `json:"requesting_device_id"`
	Body               map[string]interface{} `json:"body,omitempty"`
}

func newRoomKeyEvent(t *testing.T, action, reqID, reqDeviceID string, body map[string]interface{}) json.RawMessage {
	rkr := roomKeyRequest{
		Type: "m.room_key_request",
		Content: roomKeyRequestContent{
			Action:             action,
			RequestID:          reqID,
			RequestingDeviceID: reqDeviceID,
			Body:               body,
		},
	}
	b, err := json.Marshal(rkr)
	if err != nil {
		t.Fatalf("newRoomKeyEvent: %s", err)
	}
	return json.RawMessage(b)
}
