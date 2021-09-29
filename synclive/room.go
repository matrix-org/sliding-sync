package synclive

import (
	"encoding/json"
)

type Room struct {
	RoomID            string            `json:"room_id,omitempty"`
	Name              string            `json:"name,omitempty"`
	RequiredState     []json.RawMessage `json:"required_state,omitempty"`
	Timeline          []json.RawMessage `json:"timeline,omitempty"`
	NotificationCount int64             `json:"notification_count"`
	HighlightCount    int64             `json:"highlight_count"`
}

// SortableRoom is a room with all globally sortable fields included
// Does not include notif counts as that is user-specific.
type SortableRoom struct {
	RoomID               string
	Name                 string // by_name
	LastMessageTimestamp int64  // by_recency
	LastEventJSON        json.RawMessage
}

type SortableRooms []SortableRoom

func (s SortableRooms) Len() int64 {
	return int64(len(s))
}
func (s SortableRooms) Subslice(i, j int64) Subslicer {
	return s[i:j]
}
