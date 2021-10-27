package sync3

import (
	"encoding/json"

	"github.com/matrix-org/sync-v3/internal"
)

type Room struct {
	RoomID            string            `json:"room_id,omitempty"`
	Name              string            `json:"name,omitempty"`
	RequiredState     []json.RawMessage `json:"required_state,omitempty"`
	Timeline          []json.RawMessage `json:"timeline,omitempty"`
	NotificationCount int64             `json:"notification_count"`
	HighlightCount    int64             `json:"highlight_count"`
}

type SortableRooms []internal.RoomMetadata

func (s SortableRooms) Len() int64 {
	return int64(len(s))
}
func (s SortableRooms) Subslice(i, j int64) Subslicer {
	return s[i:j]
}
