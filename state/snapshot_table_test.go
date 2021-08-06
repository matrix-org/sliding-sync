package state

import (
	"reflect"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

func TestSnapshotTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Rollback()
	table := NewSnapshotsTable(db)

	// Insert a snapshot
	want := &SnapshotRow{
		RoomID: "A",
		Events: pq.Int64Array{1, 2, 3, 4, 5, 6, 7},
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
	if !reflect.DeepEqual(got.Events, want.Events) {
		t.Errorf("mismatched events, got: %+v want: %+v", got.Events, want.Events)
	}

	// Delete the snapshot
	err = table.Delete(txn, []int64{want.SnapshotID})
	if err != nil {
		t.Fatalf("failed to delete snapshot: %s", err)
	}
}
