package state

import (
	"database/sql"
	"sync"
	"testing"
)

func TestSnapshotRefCountTable(t *testing.T) {
	table := NewSnapshotRefCountsTable("user=kegan dbname=syncv3 sslmode=disable")
	tx, err := table.db.Begin()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	snapshotID := 100
	assertIncrement(t, tx, table, snapshotID, 1)
	assertIncrement(t, tx, table, snapshotID, 2)
	assertDecrement(t, tx, table, snapshotID, 1)
	assertDecrement(t, tx, table, snapshotID, 0)
	tx.Commit()
}

func TestSnapshotRefCountTableConcurrent(t *testing.T) {
	table := NewSnapshotRefCountsTable("user=kegan dbname=syncv3 sslmode=disable")
	tx1, err := table.db.Begin()
	if err != nil {
		t.Fatalf("failed to start txn1: %s", err)
	}
	tx2, err := table.db.Begin()
	if err != nil {
		t.Fatalf("failed to start txn2: %s", err)
	}
	snapshotID := 101

	// two goroutines increment by two, one should be blocked behind the other
	// so we should get 4 as the value
	var wg sync.WaitGroup
	wg.Add(2)
	incrBy2 := func(tx *sql.Tx) {
		defer wg.Done()
		_, err = table.Increment(tx, snapshotID)
		if err != nil {
			t.Fatalf("failed to increment: %s", err)
		}
		_, err = table.Increment(tx, snapshotID)
		if err != nil {
			t.Fatalf("failed to increment: %s", err)
		}
		err = tx.Commit()
		if err != nil {
			t.Fatalf("failed to commit: %s", err)
		}
	}

	go incrBy2(tx1)
	go incrBy2(tx2)
	wg.Wait()

	tx3, err := table.db.Begin()
	if err != nil {
		t.Fatalf("failed to start txn3: %s", err)
	}
	// 4-1=3
	assertDecrement(t, tx3, table, snapshotID, 3)
	tx3.Commit()
}

func assertIncrement(t *testing.T, tx *sql.Tx, table *SnapshotRefCountsTable, snapshotID int, wantVal int) {
	t.Helper()
	count, err := table.Increment(tx, snapshotID)
	if err != nil {
		t.Fatalf("failed to increment: %s", err)
	}
	if count != wantVal {
		t.Fatalf("count didn't increment to %v: got %v", wantVal, count)
	}
}

func assertDecrement(t *testing.T, tx *sql.Tx, table *SnapshotRefCountsTable, snapshotID int, wantVal int) {
	t.Helper()
	count, err := table.Decrement(tx, snapshotID)
	if err != nil {
		t.Fatalf("failed to decrement: %s", err)
	}
	if count != wantVal {
		t.Fatalf("count didn't decrement to %v: got %v", wantVal, count)
	}
}
