package state

import (
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
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

// Test that reserving snapshot IDs gives us exclusive access to those snapshot IDs.
// For starters, we don't do anything sequential.
func TestReservingSnapshotIDsSequential(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	table := NewSnapshotsTable(db)

	// Insert two dummy snapshots
	row1 := SnapshotRow{
		RoomID:           "!room1",
		OtherEvents:      pq.Int64Array{1, 2, 3, 4},
		MembershipEvents: pq.Int64Array{1, 4},
	}
	row2 := SnapshotRow{
		RoomID:           "!room2",
		OtherEvents:      pq.Int64Array{5, 6, 7, 8},
		MembershipEvents: pq.Int64Array{5, 8},
	}
	err := sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		err := table.Insert(txn, &row1)
		if err != nil {
			return err
		}
		err = table.Insert(txn, &row2)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if !(row2.SnapshotID > row1.SnapshotID) {
		t.Fatalf("Expected row2.snapshotID %d to be greater than row1.SnapshotID %d", row2.SnapshotID, row1.SnapshotID)
	}

	// Reserve 5 IDs
	const reserveCount = 5
	var ids []int64
	err = sqlutil.WithTransaction(db, func(txn *sqlx.Tx) (err error) {
		ids, err = table.ReserveSnapshotIDs(txn, reserveCount)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check that all IDs are distinct
	uniqueIDs := make(map[int64]struct{}, reserveCount)
	for _, id := range ids {
		uniqueIDs[id] = struct{}{}
	}
	if len(uniqueIDs) != reserveCount {
		t.Fatalf("Expected %d unique IDs in %v, but got %d", reserveCount, ids, len(uniqueIDs))
	}

	for index, id := range ids {
		if !(id > row2.SnapshotID) {
			t.Fatalf("Expected returned id#%d %d to be greater than row2.SnapshotID %d", index, id, row2.SnapshotID)
		}
	}

	// Insert a third row.
	row3 := SnapshotRow{
		RoomID:           "!room3",
		OtherEvents:      pq.Int64Array{},
		MembershipEvents: pq.Int64Array{},
	}
	err = sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		return table.Insert(txn, &row3)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check that its ID is larger than the reserved IDs
	for index, id := range ids {
		if !(row3.SnapshotID >= id) {
			t.Fatalf("Expected row3.SnapshotID %d to be greater than reserved ID#%d %d", row3.SnapshotID, index, id)
		}
	}
}

// Like TestReservingSnapshotIDs but where another transaction also requests snapshot
// IDs concurrently.
func TestReservingSnapshotIDsConcurrent(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	table := NewSnapshotsTable(db)

	// Insert two dummy snapshots
	row1 := SnapshotRow{
		RoomID:           "!room1",
		OtherEvents:      pq.Int64Array{1, 2, 3, 4},
		MembershipEvents: pq.Int64Array{1, 4},
	}
	row2 := SnapshotRow{
		RoomID:           "!room2",
		OtherEvents:      pq.Int64Array{5, 6, 7, 8},
		MembershipEvents: pq.Int64Array{5, 8},
	}
	err := sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		err := table.Insert(txn, &row1)
		if err != nil {
			return err
		}
		err = table.Insert(txn, &row2)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if !(row2.SnapshotID > row1.SnapshotID) {
		t.Fatalf("Expected row2.snapshotID %d to be greater than row1.SnapshotID %d", row2.SnapshotID, row1.SnapshotID)
	}

	// Open transaction A and reserve 5 IDs
	const reserveCount = 5
	var ids []int64
	txn1, err := db.Beginx()
	defer txn1.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	ids, err = table.ReserveSnapshotIDs(txn1, reserveCount)
	if err != nil {
		t.Fatal(err)
	}

	// Check that all IDs are distinct
	uniqueIDs := make(map[int64]struct{}, reserveCount)
	for _, id := range ids {
		uniqueIDs[id] = struct{}{}
	}
	if len(uniqueIDs) != reserveCount {
		t.Fatalf("Expected %d unique IDs in %v, but got %d", reserveCount, ids, len(uniqueIDs))
	}

	for index, id := range ids {
		if !(id > row2.SnapshotID) {
			t.Fatalf("Expected returned id#%d %d to be greater than row2.SnapshotID %d", index, id, row2.SnapshotID)
		}
	}

	// Open transaction 2 and insert a third row.
	row3 := SnapshotRow{
		RoomID:           "!room3",
		OtherEvents:      pq.Int64Array{},
		MembershipEvents: pq.Int64Array{},
	}
	txn2, err := db.Beginx()
	defer txn2.Rollback()
	if err != nil {
		t.Fatal(err)
	}

	err = table.Insert(txn2, &row3)
	if err != nil {
		t.Fatal(err)
	}

	// Check that its ID is larger than the reserved IDs
	for index, id := range ids {
		if !(row3.SnapshotID >= id) {
			t.Fatalf("Expected row3.SnapshotID %d to be greater than reserved ID#%d %d", row3.SnapshotID, index, id)
		}
	}

	// Add a fourth row to txn 1.
	row4 := SnapshotRow{
		RoomID:           "!room4",
		OtherEvents:      pq.Int64Array{},
		MembershipEvents: pq.Int64Array{},
	}
	err = table.Insert(txn1, &row4)
	if err != nil {
		t.Fatal(err)
	}

	// Check that its ID is larger still
	if !(row4.SnapshotID >= row3.SnapshotID) {
		t.Fatalf("Expected row4.SnapshotID %d to be greater than row3.SnapshotID %d", row4.SnapshotID, row3.SnapshotID)
	}
}
