package extensions

import (
	"context"
	"encoding/json"

	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

// Client created request params
type ReceiptsRequest struct {
	Enableable
}

func (r *ReceiptsRequest) Name() string {
	return "ReceiptsRequest"
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

func (r *ReceiptsRequest) ProcessLive(ctx context.Context, res *Response, extCtx Context, up caches.Update) {
	switch update := up.(type) {
	case *caches.ReceiptUpdate:
		// a live receipt event happened, send this back
		if res.Receipts == nil {
			res.Receipts = &ReceiptsResponse{
				Rooms: map[string]json.RawMessage{
					update.RoomID(): update.EphemeralEvent,
				},
			}
		} else {
			// aggregate receipts
			res.Receipts.Rooms[update.RoomID()] = update.EphemeralEvent
		}
		return
	}
}

func (r *ReceiptsRequest) ProcessInitial(ctx context.Context, res *Response, extCtx Context) {
	// grab receipts for all timelines for all the rooms we're going to return
	rooms := make(map[string]json.RawMessage)
	for roomID, timeline := range extCtx.RoomIDToTimeline {
		receipts, err := extCtx.Store.ReceiptTable.SelectReceiptsForEvents(roomID, timeline)
		if err != nil {
			logger.Err(err).Str("user", extCtx.UserID).Str("room", roomID).Msg("failed to SelectReceiptsForEvents")
			continue
		}
		// always include your own receipts
		ownReceipts, err := extCtx.Store.ReceiptTable.SelectReceiptsForUser(roomID, extCtx.UserID)
		if err != nil {
			logger.Err(err).Str("user", extCtx.UserID).Str("room", roomID).Msg("failed to SelectReceiptsForUser")
			continue
		}
		if len(receipts) == 0 && len(ownReceipts) == 0 {
			continue
		}
		rooms[roomID], _ = state.PackReceiptsIntoEDU(append(receipts, ownReceipts...))
	}
	if len(rooms) > 0 {
		res.Receipts = &ReceiptsResponse{
			Rooms: rooms, // TODO aggregate
		}
	}
}
