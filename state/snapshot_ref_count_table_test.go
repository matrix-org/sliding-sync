package state

import (
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestSnapshotRefCountTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	table := NewSnapshotRefCountsTable(db)
	tx, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer tx.Rollback()
	var snapshotID int64 = 1010101
	roomID := "!TestSnapshotRefCountTable:localhost"
	// add 3 refs to the same snapshot then migrate one and check
	moveSnapshotRef(t, tx, table, "server", roomID, snapshotID)
	moveSnapshotRef(t, tx, table, "alice", roomID, snapshotID)
	moveSnapshotRef(t, tx, table, "bob", roomID, snapshotID)
	moveSnapshotRef(t, tx, table, "server", roomID, snapshotID+1)

	// make sure the ref counts are correct
	checkNumRefs(t, tx, table, snapshotID, 2)     // alice and bob
	checkNumRefs(t, tx, table, snapshotID+1, 1)   // server was migrated to here
	checkNumRefs(t, tx, table, snapshotID+999, 0) // bad snapshot ID = 0
}

func moveSnapshotRef(t *testing.T, tx *sqlx.Tx, table *SnapshotRefCountsTable, entity, roomID string, snapID int64) {
	t.Helper()
	err := table.MoveSnapshotRefForEntity(tx, entity, roomID, snapID)
	if err != nil {
		t.Fatalf("MoveSnapshotRefForEntity: %s", err)
	}
}

func checkNumRefs(t *testing.T, tx *sqlx.Tx, table *SnapshotRefCountsTable, snapID int64, wantCount int) {
	t.Helper()
	gotCount, err := table.NumRefs(tx, snapID)
	if err != nil {
		t.Fatalf("NumRefs: %s", err)
	}
	if wantCount != gotCount {
		t.Errorf("NumRefs got %d want %d", gotCount, wantCount)
	}
}
