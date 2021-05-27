package state

import (
	"database/sql"

	"github.com/jmoiron/sqlx"
)

// RoomsTable stores the current snapshot for a room.
type RoomsTable struct {
}

func NewRoomsTable(db *sqlx.DB) *RoomsTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_rooms (
		room_id TEXT NOT NULL PRIMARY KEY,
		current_snapshot_id BIGINT NOT NULL
	);
	`)
	return &RoomsTable{}
}

func (t *RoomsTable) UpdateCurrentSnapshotID(txn *sqlx.Tx, roomID string, snapshotID int) (err error) {
	_, err = txn.Exec(`
	INSERT INTO syncv3_rooms(room_id, current_snapshot_id) VALUES($1, $2)
	ON CONFLICT (room_id) DO UPDATE SET current_snapshot_id = $2`, roomID, snapshotID)
	return err
}

func (t *RoomsTable) CurrentSnapshotID(txn *sqlx.Tx, roomID string) (snapshotID int, err error) {
	err = txn.QueryRow(`SELECT current_snapshot_id FROM syncv3_rooms WHERE room_id=$1`, roomID).Scan(&snapshotID)
	if err == sql.ErrNoRows {
		err = nil
	}
	return
}
