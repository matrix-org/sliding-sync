package internal

type Receipt struct {
	RoomID    string `db:"room_id"`
	EventID   string `db:"event_id"`
	UserID    string `db:"user_id"`
	TS        int64  `db:"ts"`
	ThreadID  string `db:"thread_id"`
	IsPrivate bool
}
