package state

import (
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// RoomsTable stores the current snapshot for a room.
type RoomsTable struct{}

func NewRoomsTable(db *sqlx.DB) *RoomsTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_rooms (
		room_id TEXT NOT NULL PRIMARY KEY,
		current_snapshot_id BIGINT NOT NULL,
		is_encrypted BOOL NOT NULL DEFAULT FALSE,
		is_tombstoned BOOL NOT NULL DEFAULT FALSE
	);
	`)
	return &RoomsTable{}
}

func (t *RoomsTable) SelectTombstonedRooms(txn *sqlx.Tx) (tombstones []string, err error) {
	err = txn.Select(&tombstones, `SELECT room_id FROM syncv3_rooms WHERE is_tombstoned=TRUE`)
	return
}

func (t *RoomsTable) SelectEncryptedRooms(txn *sqlx.Tx) (encrypted []string, err error) {
	err = txn.Select(&encrypted, `SELECT room_id FROM syncv3_rooms WHERE is_encrypted=TRUE`)
	return
}

func (t *RoomsTable) UpdateCurrentAfterSnapshotID(txn *sqlx.Tx, roomID string, snapshotID int64, isEncrypted, isTombstoned bool) (err error) {
	// This is a bit of a wonky query to ensure that you cannot set is_encrypted=false after it has been
	// set to true.
	cols := "room_id, current_snapshot_id"
	vals := "$1, $2"
	doUpdate := "ON CONFLICT (room_id) DO UPDATE SET current_snapshot_id = $2"
	if isEncrypted {
		cols += ", is_encrypted"
		vals += ", true"
		doUpdate += ", is_encrypted = true"
	}
	if isTombstoned {
		cols += ", is_tombstoned"
		vals += ", true"
		doUpdate += ", is_tombstoned = true"
	}
	insertQuery := fmt.Sprintf(`INSERT INTO syncv3_rooms(%s) VALUES(%s) %s`, cols, vals, doUpdate)
	// flip it to true.
	_, err = txn.Exec(insertQuery, roomID, snapshotID)
	return err
}

// Return the snapshot for this room AFTER the latest event has been applied.
func (t *RoomsTable) CurrentAfterSnapshotID(txn *sqlx.Tx, roomID string) (snapshotID int64, err error) {
	err = txn.QueryRow(`SELECT current_snapshot_id FROM syncv3_rooms WHERE room_id=$1`, roomID).Scan(&snapshotID)
	if err == sql.ErrNoRows {
		err = nil
	}
	return
}
