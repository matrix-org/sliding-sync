package state

import (
	"github.com/jmoiron/sqlx"
)

// UnreadTable stores unread counts per-user
type UnreadTable struct {
	db *sqlx.DB
}

func NewUnreadTable(db *sqlx.DB) *UnreadTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_unread (
		room_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		notification_count BIGINT NOT NULL DEFAULT 0,
		highlight_count BIGINT NOT NULL DEFAULT 0,
		UNIQUE(user_id, room_id)
	);
	`)
	return &UnreadTable{db}
}

func (t *UnreadTable) SelectAllNonZeroCountsForUser(userID string, callback func(roomID string, highlightCount, notificationCount int)) error {
	rows, err := t.db.Query(
		`SELECT room_id, notification_count, highlight_count FROM syncv3_unread WHERE user_id=$1 AND (notification_count > 0 OR highlight_count > 0)`,
		userID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var roomID string
		var highlightCount int
		var notifCount int
		if err := rows.Scan(&roomID, &notifCount, &highlightCount); err != nil {
			return err
		}
		callback(roomID, highlightCount, notifCount)
	}
	return nil
}

func (t *UnreadTable) SelectUnreadCounters(userID, roomID string) (highlightCount, notificationCount int, err error) {
	err = t.db.QueryRow(
		`SELECT notification_count, highlight_count FROM syncv3_unread WHERE user_id=$1 AND room_id=$2`, userID, roomID,
	).Scan(&notificationCount, &highlightCount)
	return
}

func (t *UnreadTable) UpdateUnreadCounters(userID, roomID string, highlightCount, notificationCount *int) error {
	var err error
	if highlightCount != nil && notificationCount != nil {
		_, err = t.db.Exec(
			`INSERT INTO syncv3_unread(room_id, user_id, notification_count, highlight_count) VALUES($1, $2, $3, $4)
		ON CONFLICT (room_id, user_id) DO UPDATE SET notification_count = $3, highlight_count = $4`,
			roomID, userID, *notificationCount, *highlightCount,
		)
	} else if highlightCount != nil {
		_, err = t.db.Exec(
			`INSERT INTO syncv3_unread(room_id, user_id, highlight_count) VALUES($1, $2, $3)
		ON CONFLICT (room_id, user_id) DO UPDATE SET highlight_count = $3`,
			roomID, userID, *highlightCount,
		)
	} else if notificationCount != nil {
		_, err = t.db.Exec(
			`INSERT INTO syncv3_unread(room_id, user_id, notification_count) VALUES($1, $2, $3)
		ON CONFLICT (room_id, user_id) DO UPDATE SET notification_count = $3`,
			roomID, userID, *notificationCount,
		)
	}
	return err
}
