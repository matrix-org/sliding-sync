package state

import (
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type RoomInfo struct {
	ID           string  `db:"room_id"`
	IsEncrypted  bool    `db:"is_encrypted"`
	IsTombstoned bool    `db:"is_tombstoned"`
	Type         *string `db:"type"`
}

// RoomsTable stores the current snapshot for a room.
type RoomsTable struct{}

func NewRoomsTable(db *sqlx.DB) *RoomsTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_rooms (
		room_id TEXT NOT NULL PRIMARY KEY,
		current_snapshot_id BIGINT NOT NULL,
		is_encrypted BOOL NOT NULL DEFAULT FALSE,
		is_tombstoned BOOL NOT NULL DEFAULT FALSE,
		latest_nid BIGINT NOT NULL DEFAULT 0,
		type TEXT -- nullable
	);
	`)
	return &RoomsTable{}
}

func (t *RoomsTable) SelectRoomInfos(txn *sqlx.Tx) (infos []RoomInfo, err error) {
	err = txn.Select(&infos, `SELECT room_id, is_encrypted, is_tombstoned, type FROM syncv3_rooms`)
	return
}

func (t *RoomsTable) Upsert(txn *sqlx.Tx, info RoomInfo, snapshotID, latestNID int64) (err error) {
	// This is a bit of a wonky query to ensure that you cannot set is_encrypted=false after it has been
	// set to true.
	cols := "room_id, current_snapshot_id, latest_nid"
	vals := "$1, $2, $3"
	doUpdate := "ON CONFLICT (room_id) DO UPDATE SET current_snapshot_id = $2, latest_nid = $3"
	if info.IsEncrypted {
		cols += ", is_encrypted"
		vals += ", true"
		doUpdate += ", is_encrypted = true" // flip it to true.
	}
	if info.IsTombstoned {
		cols += ", is_tombstoned"
		vals += ", true"
		doUpdate += ", is_tombstoned = true" // flip it to true.
	}
	if info.Type != nil {
		// by default we insert NULL which means no type, so we only need to set it when this is non-nil.
		// This also neatly handles the case where we issue updates which will have the Type set to nil
		// as we don't pull out the create event for incremental updates.
		cols += ", type"
		vals += ", $4"
		doUpdate += ", type = $4" // should never be updated but you never know...
	}
	insertQuery := fmt.Sprintf(`INSERT INTO syncv3_rooms(%s) VALUES(%s) %s`, cols, vals, doUpdate)
	if info.Type == nil {
		_, err = txn.Exec(insertQuery, info.ID, snapshotID, latestNID)
	} else {
		_, err = txn.Exec(insertQuery, info.ID, snapshotID, latestNID, info.Type)
	}
	return err
}

func (t *RoomsTable) LatestNIDs(txn *sqlx.Tx, roomIDs []string) (nids map[string]int64, err error) {
	nids = make(map[string]int64, len(roomIDs))
	rows, err := txn.Query(`SELECT room_id, latest_nid FROM syncv3_rooms WHERE room_id = ANY($1)`, pq.StringArray(roomIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roomID string
	var latestNID int64
	for rows.Next() {
		if err = rows.Scan(&roomID, &latestNID); err != nil {
			return nil, err
		}
		nids[roomID] = latestNID
	}
	return
}

// Return the snapshot for this room AFTER the latest event has been applied.
func (t *RoomsTable) CurrentAfterSnapshotID(txn *sqlx.Tx, roomID string) (snapshotID int64, err error) {
	err = txn.QueryRow(`SELECT current_snapshot_id FROM syncv3_rooms WHERE room_id=$1`, roomID).Scan(&snapshotID)
	if err == sql.ErrNoRows {
		err = nil
	}
	return
}
