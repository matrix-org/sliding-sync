package state

import (
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestSnapshotRefCountTable(t *testing.T) {
	db, err := sqlx.Open("postgres", "user=kegan dbname=syncv3 sslmode=disable")
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	table := NewSnapshotRefCountsTable(db)
	tx, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer tx.Rollback()
	snapshotID := 100
	assertIncrement(t, tx, table, snapshotID, 1)
	assertIncrement(t, tx, table, snapshotID, 2)
	assertDecrement(t, tx, table, snapshotID, 1)
	assertDecrement(t, tx, table, snapshotID, 0)
}

func TestSnapshotRefCountTableConcurrent(t *testing.T) {
	db, err := sqlx.Open("postgres", "user=kegan dbname=syncv3 sslmode=disable")
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	table := NewSnapshotRefCountsTable(db)
	tx1, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn1: %s", err)
	}
	tx2, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn2: %s", err)
	}
	snapshotID := 101

	// two goroutines increment by two, one should be blocked behind the other
	// so we should get 4 as the value
	var wg sync.WaitGroup
	wg.Add(2)
	incrBy2 := func(tx *sqlx.Tx) {
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

	tx3, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn3: %s", err)
	}
	// 4-1=3
	assertDecrement(t, tx3, table, snapshotID, 3)
	assertDecrement(t, tx3, table, snapshotID, 2)
	assertDecrement(t, tx3, table, snapshotID, 1)
	assertDecrement(t, tx3, table, snapshotID, 0)
	tx3.Commit()
}

func assertIncrement(t *testing.T, tx *sqlx.Tx, table *SnapshotRefCountsTable, snapshotID int, wantVal int) {
	t.Helper()
	count, err := table.Increment(tx, snapshotID)
	if err != nil {
		t.Fatalf("failed to increment: %s", err)
	}
	if count != wantVal {
		t.Fatalf("count didn't increment to %v: got %v", wantVal, count)
	}
}

func assertDecrement(t *testing.T, tx *sqlx.Tx, table *SnapshotRefCountsTable, snapshotID int, wantVal int) {
	t.Helper()
	count, err := table.Decrement(tx, snapshotID)
	if err != nil {
		t.Fatalf("failed to decrement: %s", err)
	}
	if count != wantVal {
		t.Fatalf("count didn't decrement to %v: got %v", wantVal, count)
	}
}
