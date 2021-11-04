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
	if err = table.UpdateCurrentAfterSnapshotID(txn, roomID, 100, false); err != nil {
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
	if table.UpdateCurrentAfterSnapshotID(txn, roomID, 101, false); err != nil {
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
	if err = table.UpdateCurrentAfterSnapshotID(txn, encryptedRoomID, 200, true); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// add unencrypted room
	unencryptedRoomID := "!unencrypted:localhost"
	if err = table.UpdateCurrentAfterSnapshotID(txn, unencryptedRoomID, 201, false); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// verify encrypted status
	assertEncryptedState(t, txn, table, []string{encryptedRoomID}, []string{roomID, unencryptedRoomID})

	// now flip unencrypted room
	if err = table.UpdateCurrentAfterSnapshotID(txn, unencryptedRoomID, 202, true); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// re-verify encrypted status
	assertEncryptedState(t, txn, table, []string{encryptedRoomID, unencryptedRoomID}, []string{roomID})

	// now trying to flip it to false does nothing to the encrypted status, but does update the snapshot id
	if err = table.UpdateCurrentAfterSnapshotID(txn, unencryptedRoomID, 203, false); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	id, err = table.CurrentAfterSnapshotID(txn, unencryptedRoomID)
	if err != nil {
		t.Fatalf("Failed to select current snapshot ID: %s", err)
	}
	if id != 203 {
		t.Fatalf("current snapshot id mismatch, got %d want %d", id, 203)
	}
	assertEncryptedState(t, txn, table, []string{encryptedRoomID, unencryptedRoomID}, []string{roomID})
}

func assertEncryptedState(t *testing.T, txn *sqlx.Tx, table *RoomsTable, wantEncrypted, wantUnencrypted []string) {
	t.Helper()
	gotEncrypted, gotUnencrypted, err := table.SelectEncryptedRooms(txn)
	if err != nil {
		t.Fatalf("Failed to SelectEncryptedRooms: %s", err)
	}
	sort.Strings(gotEncrypted)
	sort.Strings(gotUnencrypted)
	sort.Strings(wantEncrypted)
	sort.Strings(wantUnencrypted)
	if !reflect.DeepEqual(gotEncrypted, wantEncrypted) {
		t.Errorf("SelectEncryptedRooms: got encrypted %v want %v", gotEncrypted, wantEncrypted)
	}
	if !reflect.DeepEqual(gotUnencrypted, wantUnencrypted) {
		t.Errorf("SelectEncryptedRooms: got unencrypted %v want %v", gotUnencrypted, wantUnencrypted)
	}
}
