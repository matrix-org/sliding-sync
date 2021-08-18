package state

import (
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestRoomsTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Rollback()
	table := NewRoomsTable(db)

	// Insert 100
	if err != nil {
		t.Fatalf("Failed to start txn: %s", err)
	}
	roomID := "!1:localhost"
	if err = table.UpdateCurrentAfterSnapshotID(txn, roomID, 100); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}

	// Select 100
	id, err := table.CurrentAfterSnapshotID(txn, roomID)
	if err != nil {
		t.Fatalf("Failed to select current snapshot ID: %s", err)
	}
	if id != 100 {
		t.Fatalf("current snapshot id mismatch, got %d want %d", id, 100)
	}

	// Update to 101
	if table.UpdateCurrentAfterSnapshotID(txn, roomID, 101); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}

	// Select 101
	id, err = table.CurrentAfterSnapshotID(txn, roomID)
	if err != nil {
		t.Fatalf("Failed to select current snapshot ID: %s", err)
	}
	if id != 101 {
		t.Fatalf("current snapshot id mismatch, got %d want %d", id, 101)
	}
}
