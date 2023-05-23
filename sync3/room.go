package sync3

import (
	"encoding/json"
	"github.com/matrix-org/sliding-sync/internal"

	"github.com/matrix-org/sliding-sync/sync3/caches"
)

type Room struct {
	Name              string            `json:"name,omitempty"`
	RequiredState     []json.RawMessage `json:"required_state,omitempty"`
	Timeline          []json.RawMessage `json:"timeline,omitempty"`
	InviteState       []json.RawMessage `json:"invite_state,omitempty"`
	NotificationCount int64             `json:"notification_count"`
	HighlightCount    int64             `json:"highlight_count"`
	Initial           bool              `json:"initial,omitempty"`
	IsDM              bool              `json:"is_dm,omitempty"`
	JoinedCount       int               `json:"joined_count,omitempty"`
	InvitedCount      int               `json:"invited_count,omitempty"`
	PrevBatch         string            `json:"prev_batch,omitempty"`
	NumLive           int               `json:"num_live,omitempty"`
}

// RoomConnMetadata represents a room as seen by one specific connection (hence once
// specific device).
type RoomConnMetadata struct {
	// We enclose copies of the data kept in the global and user caches. These snapshots
	// represent the state we reported to the connection the last time they requested
	// a sync. Note that we are free to tweak fields within these copies if we want, to
	// report more appropriate data to clients that kept in the caches.
	internal.RoomMetadata
	caches.UserRoomData
}
