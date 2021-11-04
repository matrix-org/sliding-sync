package state

import (
	"database/sql"

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
		is_encrypted BOOL NOT NULL DEFAULT FALSE
	);
	`)
	return &RoomsTable{}
}

// Split rooms into ones that are encrypted and ones that are not.
func (t *RoomsTable) SelectEncryptedRooms(txn *sqlx.Tx) (encrypted, unencrypted []string, err error) {
	err = txn.Select(&encrypted, `SELECT room_id FROM syncv3_rooms WHERE is_encrypted=TRUE`)
	if err != nil {
		return
	}
	err = txn.Select(&unencrypted, `SELECT room_id FROM syncv3_rooms WHERE is_encrypted=FALSE`)
	return
}

func (t *RoomsTable) UpdateCurrentAfterSnapshotID(txn *sqlx.Tx, roomID string, snapshotID int64, isEncrypted bool) (err error) {
	// don't touch is_encrypted if we say it is false
	if !isEncrypted {
		_, err = txn.Exec(`
	INSERT INTO syncv3_rooms(room_id, current_snapshot_id) VALUES($1, $2)
	ON CONFLICT (room_id) DO UPDATE SET current_snapshot_id = $2`, roomID, snapshotID)
	} else {
		// flip it to true. This is a bit of a wonky query to ensure that you cannot set is_encrypted=false after it has been
		// set to true.
		_, err = txn.Exec(`
		INSERT INTO syncv3_rooms(room_id, current_snapshot_id, is_encrypted) VALUES($1, $2, true)
		ON CONFLICT (room_id) DO UPDATE SET current_snapshot_id = $2, is_encrypted = true`, roomID, snapshotID)
	}
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
