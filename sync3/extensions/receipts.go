package extensions

import (
	"encoding/json"

	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

// Client created request params
type ReceiptsRequest struct {
	Enabled bool `json:"enabled"`
}

func (r ReceiptsRequest) ApplyDelta(next *ReceiptsRequest) *ReceiptsRequest {
	r.Enabled = next.Enabled
	return &r
}

// Server response
type ReceiptsResponse struct {
	// room_id -> m.receipt ephemeral event
	Rooms map[string]json.RawMessage `json:"rooms,omitempty"`
}

func (r *ReceiptsResponse) HasData(isInitial bool) bool {
	if isInitial {
		return true
	}
	return len(r.Rooms) > 0
}

func ProcessLiveReceipts(up caches.Update, updateWillReturnResponse bool, userID string, req *ReceiptsRequest) (res *ReceiptsResponse) {
	switch update := up.(type) {
	case *caches.ReceiptUpdate:
		// a live receipt event happened, send this back
		return &ReceiptsResponse{
			Rooms: map[string]json.RawMessage{
				update.RoomID(): update.EphemeralEvent,
			},
		}
	}
	return nil
}

func ProcessReceipts(store *state.Storage, roomIDToTimeline map[string][]string, userID string, isInitial bool, req *ReceiptsRequest) (res *ReceiptsResponse) {
	// grab receipts for all timelines for all the rooms we're going to return
	res = &ReceiptsResponse{
		Rooms: make(map[string]json.RawMessage),
	}
	for roomID, timeline := range roomIDToTimeline {
		receipts, err := store.ReceiptTable.SelectReceiptsForEvents(roomID, timeline)
		if err != nil {
			logger.Err(err).Str("user", userID).Str("room", roomID).Msg("failed to SelectReceiptsForEvents")
			continue
		}
		// always include your own receipts
		ownReceipts, err := store.ReceiptTable.SelectReceiptsForUser(roomID, userID)
		if err != nil {
			logger.Err(err).Str("user", userID).Str("room", roomID).Msg("failed to SelectReceiptsForUser")
			continue
		}
		if len(receipts) == 0 && len(ownReceipts) == 0 {
			continue
		}
		res.Rooms[roomID], _ = state.PackReceiptsIntoEDU(append(receipts, ownReceipts...))
	}
	return
}
