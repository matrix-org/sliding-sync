package extensions

import (
	"context"
	"encoding/json"

	"github.com/matrix-org/sliding-sync/sync3/caches"
)

// Client created request params
type TypingRequest struct {
	Enableable
}

func (r *TypingRequest) Name() string {
	return "TypingRequest"
}

// Server response
type TypingResponse struct {
	Rooms map[string]json.RawMessage `json:"rooms,omitempty"`
}

func (r *TypingResponse) HasData(isInitial bool) bool {
	if isInitial {
		return true
	}
	return len(r.Rooms) > 0
}

func (r *TypingRequest) ProcessLive(ctx context.Context, res *Response, extCtx Context, up caches.Update) {
	switch update := up.(type) {
	case *caches.TypingUpdate:
		// a live typing event happened, send this back. Allow for aggregation (>1 typing event in same room => replace)
		if res.Typing == nil {
			res.Typing = &TypingResponse{
				Rooms: make(map[string]json.RawMessage),
			}
		}
		res.Typing.Rooms[update.RoomID()] = update.GlobalRoomMetadata().TypingEvent
	case caches.RoomUpdate:
		// if this is a room update which is included in the response, send typing notifs for this room
		if _, exists := extCtx.RoomIDToTimeline[update.RoomID()]; !exists {
			return
		}
		ev := update.GlobalRoomMetadata().TypingEvent
		if ev == nil {
			return
		}
		res.Typing = &TypingResponse{ // TODO aggregate
			Rooms: map[string]json.RawMessage{
				update.RoomID(): ev,
			},
		}
	}
}

func (r *TypingRequest) ProcessInitial(ctx context.Context, res *Response, extCtx Context) {
	// grab typing users for all the rooms we're going to return
	rooms := make(map[string]json.RawMessage)
	roomIDs := make([]string, 0, len(extCtx.RoomIDToTimeline))
	for roomID := range extCtx.RoomIDToTimeline {
		roomIDs = append(roomIDs, roomID)
	}
	roomToGlobalMetadata := extCtx.GlobalCache.LoadRooms(roomIDs...)
	for roomID := range extCtx.RoomIDToTimeline {
		meta := roomToGlobalMetadata[roomID]
		if meta == nil || meta.TypingEvent == nil {
			continue
		}
		rooms[roomID] = meta.TypingEvent
	}
	if len(rooms) == 0 {
		return // don't add a typing extension, no data!
	}
	res.Typing = &TypingResponse{
		Rooms: rooms, // TODO aggregate
	}
}
