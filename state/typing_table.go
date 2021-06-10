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
	CREATE SEQUENCE IF NOT EXISTS syncv3_typing_seq;
	CREATE TABLE IF NOT EXISTS syncv3_typing (
		stream_id BIGINT NOT NULL DEFAULT nextval('syncv3_typing_seq'),
		room_id TEXT NOT NULL PRIMARY KEY,
		user_ids TEXT[] NOT NULL
	);
	`)
	return &TypingTable{db}
}

func (t *TypingTable) SetTyping(roomID string, userIDs []string) (streamID int, err error) {
	if userIDs == nil {
		userIDs = []string{}
	}
	err = t.db.QueryRow(`
		INSERT INTO syncv3_typing(room_id, user_ids) VALUES($1, $2)
		ON CONFLICT (room_id) DO UPDATE SET user_ids = $2, stream_id = nextval('syncv3_typing_seq') RETURNING stream_id`,
		roomID, pq.Array(userIDs),
	).Scan(&streamID)
	return streamID, err
}

func (t *TypingTable) Typing(roomID string, streamID int) (userIDs []string, newStreamID int, err error) {
	var userIDsArray pq.StringArray
	err = t.db.QueryRow(
		`SELECT stream_id, user_ids FROM syncv3_typing WHERE room_id=$1 AND stream_id > $2`, roomID, streamID,
	).Scan(&newStreamID, &userIDsArray)
	if err == sql.ErrNoRows {
		err = nil
	}
	return userIDsArray, newStreamID, err
}
