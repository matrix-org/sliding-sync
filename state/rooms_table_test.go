package state

import (
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestRoomsTable(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	_, err := db.Exec(`DROP TABLE IF EXISTS syncv3_rooms`)
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
	if err = table.Upsert(txn, RoomInfo{
		ID: roomID,
	}, 100, 1); err != nil {
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
	if table.Upsert(txn, RoomInfo{
		ID: roomID,
	}, 101, 1); err != nil {
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
	if err = table.Upsert(txn, RoomInfo{
		ID:          encryptedRoomID,
		IsEncrypted: true,
	}, 200, 1); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// add unencrypted room
	unencryptedRoomID := "!unencrypted:localhost"
	if err = table.Upsert(txn, RoomInfo{
		ID: unencryptedRoomID,
	}, 201, 1); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// verify encrypted status
	if !getRoomInfo(t, txn, table, encryptedRoomID).IsEncrypted {
		t.Fatalf("room %s is not encrypted when it should be", encryptedRoomID)
	}
	if getRoomInfo(t, txn, table, unencryptedRoomID).IsEncrypted {
		t.Fatalf("room %s is encrypted when it should not be", unencryptedRoomID)
	}

	// now flip unencrypted room
	if err = table.Upsert(txn, RoomInfo{
		ID:          unencryptedRoomID,
		IsEncrypted: true,
	}, 202, 1); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// re-verify encrypted status
	if !getRoomInfo(t, txn, table, unencryptedRoomID).IsEncrypted {
		t.Fatalf("room %s is not encrypted when it should be", unencryptedRoomID)
	}

	// now trying to flip it to false does nothing to the encrypted status, but does update the snapshot id
	if err = table.Upsert(txn, RoomInfo{
		ID:          unencryptedRoomID,
		IsEncrypted: false,
	}, 203, 1); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	id, err = table.CurrentAfterSnapshotID(txn, unencryptedRoomID)
	if err != nil {
		t.Fatalf("Failed to select current snapshot ID: %s", err)
	}
	if id != 203 {
		t.Fatalf("current snapshot id mismatch, got %d want %d", id, 203)
	}
	if !getRoomInfo(t, txn, table, unencryptedRoomID).IsEncrypted {
		t.Fatalf("room %s is not encrypted when it should be", unencryptedRoomID)
	}

	// now check tombstones in the same way (TODO: dedupe)
	// add tombstoned room
	tombstonedRoomID := "!tombstoned:localhost"
	upgradedRoomID := "!upgraded:localhost"
	if err = table.Upsert(txn, RoomInfo{
		ID:             tombstonedRoomID,
		UpgradedRoomID: &upgradedRoomID,
	}, 300, 1001); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// add untombstoned room
	untombstonedRoomID := "!untombstoned:localhost"
	if err = table.Upsert(txn, RoomInfo{
		ID: untombstonedRoomID,
	}, 301, 1002); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// verify tombstone status
	if getRoomInfo(t, txn, table, untombstonedRoomID).UpgradedRoomID != nil {
		t.Fatalf("room %s is tombstoned when it shouldn't be", untombstonedRoomID)
	}
	if ri := getRoomInfo(t, txn, table, tombstonedRoomID); ri.UpgradedRoomID == nil || *ri.UpgradedRoomID != upgradedRoomID {
		t.Fatalf("room %s is not tombstoned when it should be", tombstonedRoomID)
	}

	// now flip tombstone
	upgradedRoomID2 := "!upgraded2:localhost"
	if err = table.Upsert(txn, RoomInfo{
		ID:             untombstonedRoomID,
		UpgradedRoomID: &upgradedRoomID2,
	}, 302, 1003); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// re-verify tombstone status
	if getRoomInfo(t, txn, table, untombstonedRoomID).UpgradedRoomID == nil {
		t.Fatalf("room %s is not tombstoned when it should be", untombstonedRoomID)
	}

	// check encrypted and tombstone can be set at once
	both := "!both:localhost"
	if err = table.Upsert(txn, RoomInfo{
		ID:             both,
		IsEncrypted:    true,
		UpgradedRoomID: &upgradedRoomID2,
	}, 303, 1); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	info := getRoomInfo(t, txn, table, both)
	if !info.IsEncrypted || info.UpgradedRoomID == nil {
		t.Fatalf("Setting both tombstone/encrypted did not work, got: %+v", info)
	}

	// check type can be set
	spaceRoomID := "!space:localhost"
	spaceType := "m.space"
	if err = table.Upsert(txn, RoomInfo{
		ID:   spaceRoomID,
		Type: &spaceType,
	}, 999, 1); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	info = getRoomInfo(t, txn, table, spaceRoomID)
	if info.Type == nil || *info.Type != spaceType {
		t.Fatalf("set type to %s but retrieved %v", spaceType, info.Type)
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

	// check predecessor can be set
	sucessorRoomID := "!sucessorRoomID:localhost"
	predecessorRoomID := "!predecessor:localhost"
	if err = table.Upsert(txn, RoomInfo{
		ID:                sucessorRoomID,
		PredecessorRoomID: &predecessorRoomID,
		IsEncrypted:       true,
	}, 2222, 1); err != nil {
		t.Fatalf("Failed to update current snapshot ID: %s", err)
	}
	// verify
	ri := getRoomInfo(t, txn, table, sucessorRoomID)
	if !ri.IsEncrypted {
		t.Errorf("upgraded room did not set encryption bit")
	}
	if ri.PredecessorRoomID == nil {
		t.Errorf("predecessor room is nil")
	}
	if *ri.PredecessorRoomID == sucessorRoomID {
		t.Errorf("predecessor room got %v want %v", *ri.PredecessorRoomID, predecessorRoomID)
	}

}

func getRoomInfo(t *testing.T, txn *sqlx.Tx, table *RoomsTable, roomID string) RoomInfo {
	t.Helper()
	infos, err := table.SelectRoomInfos(txn)
	if err != nil {
		t.Fatalf("SelectRoomInfos: %s", err)
	}
	for _, inf := range infos {
		if inf.ID == roomID {
			return inf
		}
	}
	t.Fatalf("failed to find RoomInfo for room ID: %s", roomID)
	return RoomInfo{}
}
