package state

import (
	"database/sql"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// TypingTable stores who is currently typing
// TODO: If 2 users are in the same room and 1 is on a laggy synchotron, we'll flip flop who is
// typing with live / stale data. Maybe do this per user per room?
type TypingTable struct {
	db *sqlx.DB
}

func NewTypingTable(db *sqlx.DB) *TypingTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_typing (
		room_id TEXT NOT NULL PRIMARY KEY,
		user_ids TEXT[] NOT NULL
	);
	`)
	return &TypingTable{db}
}

func (t *TypingTable) SetTyping(roomID string, userIDs []string) (err error) {
	if userIDs == nil {
		userIDs = []string{}
	}
	_, err = t.db.Exec(`
	INSERT INTO syncv3_typing(room_id, user_ids) VALUES($1, $2)
	ON CONFLICT (room_id) DO UPDATE SET user_ids = $2`, roomID, pq.Array(userIDs))
	return err
}

func (t *TypingTable) Typing(roomID string) (userIDs []string, err error) {
	var userIDsArray pq.StringArray
	err = t.db.QueryRow(`SELECT user_ids FROM syncv3_typing WHERE room_id=$1`, roomID).Scan(&userIDsArray)
	if err == sql.ErrNoRows {
		err = nil
	}
	return userIDsArray, err
}
