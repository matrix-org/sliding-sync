package sync3

import (
	"encoding/json"

	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sync3/caches"
)

type Room struct {
	RoomID            string            `json:"room_id,omitempty"`
	Name              string            `json:"name,omitempty"`
	RequiredState     []json.RawMessage `json:"required_state,omitempty"`
	Timeline          []json.RawMessage `json:"timeline,omitempty"`
	InviteState       []json.RawMessage `json:"invite_state,omitempty"`
	NotificationCount int64             `json:"notification_count"`
	HighlightCount    int64             `json:"highlight_count"`
	Initial           bool              `json:"initial,omitempty"`
	IsDM              bool              `json:"is_dm,omitempty"`
	PrevBatch         string            `json:"prev_batch,omitempty"`
}

type RoomConnMetadata struct {
	internal.RoomMetadata
	caches.UserRoomData

	CanonicalisedName string // stripped leading symbols like #, all in lower case
}
