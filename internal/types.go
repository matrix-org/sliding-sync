package internal

import "encoding/json"

type Receipt struct {
	RoomID    string `db:"room_id"`
	EventID   string `db:"event_id"`
	UserID    string `db:"user_id"`
	TS        int64  `db:"ts"`
	ThreadID  string `db:"thread_id"`
	IsPrivate bool
}

type TimelineResponse struct {
	Events    []json.RawMessage `json:"events"`
	Limited   bool              `json:"limited"`
	PrevBatch string            `json:"prev_batch,omitempty"`
}
