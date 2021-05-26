package state

import (
	"reflect"
	"testing"

	"github.com/lib/pq"
)

func TestSnapshotTable(t *testing.T) {
	table := NewSnapshotsTable("user=kegan dbname=syncv3 sslmode=disable")
	want := &SnapshotRow{
		RoomID: "A",
		Events: pq.Int64Array{1, 2, 3, 4, 5, 6, 7},
	}
	err := table.Insert(want)
	if err != nil {
		t.Fatalf("Failed to insert: %s", err)
	}
	if want.SnapshotID == 0 {
		t.Fatalf("Snapshot ID not set")
	}
	got, err := table.Select(want.SnapshotID)
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
}
