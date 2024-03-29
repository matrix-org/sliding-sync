package state

import (
	"reflect"
	"testing"

	"github.com/lib/pq"
)

func TestSnapshotTable(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Rollback()
	table := NewSnapshotsTable(db)

	// Insert a snapshot
	want := &SnapshotRow{
		RoomID:           "A",
		OtherEvents:      pq.Int64Array{1, 3, 5, 7},
		MembershipEvents: pq.Int64Array{2, 4, 6},
	}
	err = table.Insert(txn, want)
	if err != nil {
		t.Fatalf("Failed to insert: %s", err)
	}
	if want.SnapshotID == 0 {
		t.Fatalf("Snapshot ID not set")
	}

	// Select the same snapshot and assert
	got, err := table.Select(txn, want.SnapshotID)
	if err != nil {
		t.Fatalf("Failed to select: %s", err)
	}
	if got.SnapshotID != want.SnapshotID {
		t.Errorf("mismatched snapshot IDs, got %v want %v", got.SnapshotID, want.SnapshotID)
	}
	if got.RoomID != want.RoomID {
		t.Errorf("mismatched room IDs, got %v want %v", got.RoomID, want.RoomID)
	}
	if !reflect.DeepEqual(got.MembershipEvents, want.MembershipEvents) {
		t.Errorf("mismatched membership events, got: %+v want: %+v", got.MembershipEvents, want.MembershipEvents)
	}
	if !reflect.DeepEqual(got.OtherEvents, want.OtherEvents) {
		t.Errorf("mismatched other events, got: %+v want: %+v", got.OtherEvents, want.OtherEvents)
	}

	// Delete the snapshot
	err = table.Delete(txn, []int64{want.SnapshotID})
	if err != nil {
		t.Fatalf("failed to delete snapshot: %s", err)
	}
}
