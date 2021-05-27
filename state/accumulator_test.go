package state

import (
	"encoding/json"
	"testing"
)

func TestAccumulator(t *testing.T) {
	roomID := "!1:localhost"
	roomEvents := []json.RawMessage{
		[]byte(`{"event_id":"A", "type":"m.room.create", "state_key":"", "content":{"creator":"@me:localhost"}}`),
		[]byte(`{"event_id":"B", "type":"m.room.member", "state_key":"@me:localhost", "content":{"membership":"join"}}`),
		[]byte(`{"event_id":"C", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
	}
	roomEventIDs := []string{"A", "B", "C"}
	accumulator := NewAccumulator("user=kegan dbname=syncv3 sslmode=disable")
	err := accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("falied to Initialise accumulator: %s", err)
	}

	txn, err := accumulator.db.Beginx()
	if err != nil {
		t.Fatalf("failed to start assert txn: %s", err)
	}

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
}
