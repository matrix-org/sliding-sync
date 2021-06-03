package state

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAccumulatorInitialise(t *testing.T) {
	roomID := "!TestAccumulatorInitialise:localhost"
	roomEvents := []json.RawMessage{
		[]byte(`{"event_id":"A", "type":"m.room.create", "state_key":"", "content":{"creator":"@me:localhost"}}`),
		[]byte(`{"event_id":"B", "type":"m.room.member", "state_key":"@me:localhost", "content":{"membership":"join"}}`),
		[]byte(`{"event_id":"C", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
	}
	roomEventIDs := []string{"A", "B", "C"}
	accumulator := NewAccumulator(postgresConnectionString)
	err := accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("falied to Initialise accumulator: %s", err)
	}

	txn, err := accumulator.db.Beginx()
	if err != nil {
		t.Fatalf("failed to start assert txn: %s", err)
	}
	defer txn.Rollback()

	// There should be one snapshot on the current state
	snapID, err := accumulator.roomsTable.CurrentSnapshotID(txn, roomID)
	if err != nil {
		t.Fatalf("failed to select current snapshot: %s", err)
	}
	if snapID == 0 {
		t.Fatalf("Initialise did not store a current snapshot")
	}

	// this snapshot should have 3 events in it
	row, err := accumulator.snapshotTable.Select(txn, snapID)
	if err != nil {
		t.Fatalf("failed to select snapshot %d: %s", snapID, err)
	}
	if len(row.Events) != len(roomEvents) {
		t.Fatalf("got %d events, want %d in current state snapshot", len(row.Events), len(roomEvents))
	}

	// these 3 events should map to the three events we initialised with
	events, err := accumulator.eventsTable.SelectByNIDs(txn, row.Events)
	if err != nil {
		t.Fatalf("failed to extract events in snapshot: %s", err)
	}
	if len(events) != len(roomEvents) {
		t.Fatalf("failed to extract %d events, got %d", len(roomEvents), len(events))
	}
	for i := range events {
		if events[i].ID != roomEventIDs[i] {
			t.Errorf("event %d was not stored correctly: got ID %s want %s", i, events[i].ID, roomEventIDs[i])
		}
	}

	// the ref counter should be 1 for this snapshot
	emptyRefs, err := accumulator.snapshotRefCountTable.DeleteEmptyRefs(txn)
	if err != nil {
		t.Fatalf("failed to delete empty refs: %s", err)
	}
	if len(emptyRefs) > 0 {
		t.Fatalf("got %d empty refs, want none", len(emptyRefs))
	}

	// Subsequent calls do nothing and are not an error
	err = accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("falied to Initialise accumulator: %s", err)
	}
}

func TestAccumulatorAccumulate(t *testing.T) {
	roomID := "!TestAccumulatorAccumulate:localhost"
	roomEvents := []json.RawMessage{
		[]byte(`{"event_id":"D", "type":"m.room.create", "state_key":"", "content":{"creator":"@me:localhost"}}`),
		[]byte(`{"event_id":"E", "type":"m.room.member", "state_key":"@me:localhost", "content":{"membership":"join"}}`),
		[]byte(`{"event_id":"F", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
	}
	accumulator := NewAccumulator(postgresConnectionString)
	err := accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("failed to Initialise accumulator: %s", err)
	}

	// accumulate new state makes a new snapshot and removes the old snapshot
	newEvents := []json.RawMessage{
		// non-state event does nothing
		[]byte(`{"event_id":"G", "type":"m.room.message","content":{"body":"Hello World","msgtype":"m.text"}}`),
		// join_rules should clobber the one from initialise
		[]byte(`{"event_id":"H", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
		// new state event should be added to the snapshot
		[]byte(`{"event_id":"I", "type":"m.room.history_visibility", "state_key":"", "content":{"visibility":"public"}}`),
	}
	if err = accumulator.Accumulate(roomID, newEvents); err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}

	// Begin assertions
	wantStateEvents := []json.RawMessage{
		roomEvents[0], // create event
		roomEvents[1], // member event
		newEvents[1],  // join rules
		newEvents[2],  // history visibility
	}
	txn, err := accumulator.db.Beginx()
	if err != nil {
		t.Fatalf("failed to start assert txn: %s", err)
	}
	defer txn.Rollback()

	// There should be one snapshot in current state
	snapID, err := accumulator.roomsTable.CurrentSnapshotID(txn, roomID)
	if err != nil {
		t.Fatalf("failed to select current snapshot: %s", err)
	}
	if snapID == 0 {
		t.Fatalf("Initialise or Accumulate did not store a current snapshot")
	}

	// The snapshot should have 4 events
	row, err := accumulator.snapshotTable.Select(txn, snapID)
	if err != nil {
		t.Fatalf("failed to select snapshot %d: %s", snapID, err)
	}
	if len(row.Events) != len(wantStateEvents) {
		t.Fatalf("snapshot: %d got %d events, want %d in current state snapshot", snapID, len(row.Events), len(wantStateEvents))
	}

	// these 4 events should map to the create/member events from initialise, then the join_rules/history_visibility from accumulate
	events, err := accumulator.eventsTable.SelectByNIDs(txn, row.Events)
	if err != nil {
		t.Fatalf("failed to extract events in snapshot: %s", err)
	}
	if len(events) != len(wantStateEvents) {
		t.Fatalf("failed to extract %d events, got %d", len(wantStateEvents), len(events))
	}
	for i := range events {
		if events[i].ID != gjson.GetBytes(wantStateEvents[i], "event_id").Str {
			t.Errorf("event %d was not stored correctly: got ID %s want %s", i, events[i].ID, gjson.GetBytes(wantStateEvents[i], "event_id").Str)
		}
	}

	// subsequent calls do nothing and are not an error
	if err = accumulator.Accumulate(roomID, newEvents); err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}
}

/*
func TestAccumulatorDelta(t *testing.T) {
	roomID := "!TestAccumulatorAccumulate:localhost"
	roomEvents := []json.RawMessage{
		[]byte(`{"event_id":"D", "type":"m.room.create", "state_key":"", "content":{"creator":"@me:localhost"}}`),
		[]byte(`{"event_id":"E", "type":"m.room.member", "state_key":"@me:localhost", "content":{"membership":"join"}}`),
		[]byte(`{"event_id":"F", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
	}
	accumulator := NewAccumulator(postgresConnectionString)
	err := accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("failed to Initialise accumulator: %s", err)
	}

	// accumulate new state makes a new snapshot and removes the old snapshot
	newEvents := []json.RawMessage{
		// non-state event does nothing
		[]byte(`{"event_id":"G", "type":"m.room.message","content":{"body":"Hello World","msgtype":"m.text"}}`),
		// join_rules should clobber the one from initialise
		[]byte(`{"event_id":"H", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
		// new state event should be added to the snapshot
		[]byte(`{"event_id":"I", "type":"m.room.history_visibility", "state_key":"", "content":{"visibility":"public"}}`),
	}
	if err = accumulator.Accumulate(roomID, newEvents); err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}
} */
