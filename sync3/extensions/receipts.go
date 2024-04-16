package extensions

import (
	"context"
	"encoding/json"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/rs/zerolog/log"
)

// Client created request params
type ReceiptsRequest struct {
	Core
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

func (r *ReceiptsRequest) AppendLive(ctx context.Context, res *Response, extCtx Context, up caches.Update) {
	switch update := up.(type) {
	case *caches.ReceiptUpdate:
		if !r.RoomInScope(update.RoomID(), extCtx) {
			break
		}

		// a live receipt event happened, send this back
		if res.Receipts == nil {
			edu, err := state.PackReceiptsIntoEDU([]internal.Receipt{update.Receipt})
			if err != nil {
				log.Err(err).Str("user", extCtx.UserID).Str("room", update.Receipt.RoomID).Msg("failed to pack receipt into new edu")
				internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
				return
			}
			res.Receipts = &ReceiptsResponse{
				Rooms: map[string]json.RawMessage{
					update.RoomID(): edu,
				},
			}
		} else if res.Receipts.Rooms[update.RoomID()] == nil {
			// we have receipts already, but not for this room
			edu, err := state.PackReceiptsIntoEDU([]internal.Receipt{update.Receipt})
			if err != nil {
				log.Err(err).Str("user", extCtx.UserID).Str("room", update.Receipt.RoomID).Msg("failed to pack receipt into edu")
				internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
				return
			}
			res.Receipts.Rooms[update.RoomID()] = edu
		} else {
			// we have receipts already for this room.
			// aggregate receipts: we need to unpack then repack annoyingly.
			pub, priv, err := state.UnpackReceiptsFromEDU(update.RoomID(), res.Receipts.Rooms[update.RoomID()])
			if err != nil {
				log.Err(err).Str("user", extCtx.UserID).Str("room", update.Receipt.RoomID).Msg("failed to pack receipt into edu")
				internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
				return
			}
			receipts := append(pub, priv...)
			// add the live one
			receipts = append(receipts, update.Receipt)
			edu, err := state.PackReceiptsIntoEDU(receipts)
			if err != nil {
				log.Err(err).Str("user", extCtx.UserID).Str("room", update.Receipt.RoomID).Msg("failed to pack receipt into edu")
				internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
				return
			}
			res.Receipts.Rooms[update.RoomID()] = edu
		}
	}
}

func (r *ReceiptsRequest) ProcessInitial(ctx context.Context, res *Response, extCtx Context) {
	// grab receipts for all timelines for all the rooms we're going to return
	rooms := make(map[string]json.RawMessage)
	interestedRoomIDs := make([]string, 0, len(extCtx.RoomIDToTimeline))
	otherReceipts := make(map[string][]internal.Receipt)
	for roomID, timeline := range extCtx.RoomIDToTimeline {
		if !r.RoomInScope(roomID, extCtx) {
			continue
		}
		receipts, err := extCtx.Store.ReceiptTable.SelectReceiptsForEvents(roomID, timeline)
		if err != nil {
			log.Err(err).Str("user", extCtx.UserID).Str("room", roomID).Msg("failed to SelectReceiptsForEvents")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
			continue
		}
		otherReceipts[roomID] = receipts
		interestedRoomIDs = append(interestedRoomIDs, roomID)
	}
	// single shot query to pull out our own receipts for these rooms to always include our own receipts
	ownReceipts, err := extCtx.Store.ReceiptTable.SelectReceiptsForUser(interestedRoomIDs, extCtx.UserID)
	if err != nil {
		log.Err(err).Str("user", extCtx.UserID).Strs("rooms", interestedRoomIDs).Msg("failed to SelectReceiptsForUser")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return
	}

	// move all own receipts into other receipts so we don't need to handle cases where receipts are in one map but not the other
	for roomID, ownRecs := range ownReceipts {
		otherReceipts[roomID] = append(otherReceipts[roomID], ownRecs...)
	}

	for roomID, receipts := range otherReceipts {
		if len(receipts) == 0 {
			continue
		}
		rooms[roomID], _ = state.PackReceiptsIntoEDU(receipts)
	}

	if len(rooms) > 0 {
		res.Receipts = &ReceiptsResponse{
			Rooms: rooms,
		}
	}
}
