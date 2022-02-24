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
	Initial           bool              `json:"initial,omitempty"`
}

type RoomConnMetadata struct {
	internal.RoomMetadata
	UserRoomData

	CanonicalisedName string // stripped leading symbols like #, all in lower case
}
