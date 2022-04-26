package state

import (
	"reflect"
	"sort"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestRoomsTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	_, err = db.Exec(`DROP TABLE IF EXISTS syncv3_rooms`)
	if err != nil {
		t.Fatalf("failed to drop rooms table: %s", err)
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
	if err = table.UpdateCurrentAfterSnapshotID(txn, roomID, 100, 1, false, false); err != nil {
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
	if table.UpdateCurrentAfterSnapshotID(txn, roomID, 101, 1, false, false); err != nil {
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

	// add encrypted room
	encryptedRoomID := "!encrypted:localhost"
	if err = table.UpdateCurrentAfterSnapshotID(txn, encryptedRoomID, 200, 1, true, false); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// add unencrypted room
	unencryptedRoomID := "!unencrypted:localhost"
	if err = table.UpdateCurrentAfterSnapshotID(txn, unencryptedRoomID, 201, 1, false, false); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// verify encrypted status
	encryptedRoomIDs, err := table.SelectEncryptedRooms(txn)
	assertStringsAnyOrder(t, txn, encryptedRoomIDs, []string{encryptedRoomID}, err)

	// now flip unencrypted room
	if err = table.UpdateCurrentAfterSnapshotID(txn, unencryptedRoomID, 202, 1, true, false); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// re-verify encrypted status
	encryptedRoomIDs, err = table.SelectEncryptedRooms(txn)
	assertStringsAnyOrder(t, txn, encryptedRoomIDs, []string{encryptedRoomID, unencryptedRoomID}, err)

	// now trying to flip it to false does nothing to the encrypted status, but does update the snapshot id
	if err = table.UpdateCurrentAfterSnapshotID(txn, unencryptedRoomID, 203, 1, false, false); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	id, err = table.CurrentAfterSnapshotID(txn, unencryptedRoomID)
	if err != nil {
		t.Fatalf("Failed to select current snapshot ID: %s", err)
	}
	if id != 203 {
		t.Fatalf("current snapshot id mismatch, got %d want %d", id, 203)
	}
	encryptedRoomIDs, err = table.SelectEncryptedRooms(txn)
	assertStringsAnyOrder(t, txn, encryptedRoomIDs, []string{encryptedRoomID, unencryptedRoomID}, err)

	// now check tombstones in the same way (TODO: dedupe)
	// add tombstoned room
	tombstonedRoomID := "!tombstoned:localhost"
	if err = table.UpdateCurrentAfterSnapshotID(txn, tombstonedRoomID, 300, 1001, false, true); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// add untombstoned room
	untombstonedRoomID := "!untombstoned:localhost"
	if err = table.UpdateCurrentAfterSnapshotID(txn, untombstonedRoomID, 301, 1002, false, false); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// verify tombstone status
	tombstoneRoomIDs, err := table.SelectTombstonedRooms(txn)
	assertStringsAnyOrder(t, txn, tombstoneRoomIDs, []string{tombstonedRoomID}, err)

	// now flip tombstone
	if err = table.UpdateCurrentAfterSnapshotID(txn, untombstonedRoomID, 302, 1003, false, true); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// re-verify tombstone status
	tombstoneRoomIDs, err = table.SelectTombstonedRooms(txn)
	assertStringsAnyOrder(t, txn, tombstoneRoomIDs, []string{tombstonedRoomID, untombstonedRoomID}, err)

	// check encrypted and tombstone can be set at once
	both := "!both:localhost"
	if err = table.UpdateCurrentAfterSnapshotID(txn, both, 303, 1, true, true); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}

	// check LatestNIDs
	nidMap, err := table.LatestNIDs(txn, []string{tombstonedRoomID, untombstonedRoomID})
	if err != nil {
		t.Fatalf("LatestNIDs: %s", err)
	}
	if nidMap[tombstonedRoomID] != 1001 {
		t.Errorf("LatestNIDs: got %v want 1001", nidMap[tombstonedRoomID])
	}
	if nidMap[untombstonedRoomID] != 1003 { // not 1002 as it should be updated by the subsequent update
		t.Errorf("LatestNIDs: got %v want 1003", nidMap[untombstonedRoomID])
	}

}

func assertStringsAnyOrder(t *testing.T, txn *sqlx.Tx, gots, wants []string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Failed to SelectEncryptedRooms: %s", err)
	}
	sort.Strings(gots)
	sort.Strings(wants)
	if !reflect.DeepEqual(gots, wants) {
		t.Errorf("assertStringsAnyOrder: got %v want %v", gots, wants)
	}
}
