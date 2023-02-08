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

func (r *TypingRequest) ProcessLiveTyping(extCtx Context) (res *TypingResponse) {
	switch update := extCtx.Update.(type) {
	case *caches.TypingUpdate:
		// a live typing event happened, send this back
		return &TypingResponse{
			Rooms: map[string]json.RawMessage{
				update.RoomID(): update.GlobalRoomMetadata().TypingEvent,
			},
		}
	case caches.RoomUpdate:
		// if this is a room update which is included in the response, send typing notifs for this room
		if _, exists := extCtx.RoomIDToTimeline[update.RoomID()]; !exists {
			return nil
		}
		ev := update.GlobalRoomMetadata().TypingEvent
		if ev == nil {
			return nil
		}
		return &TypingResponse{
			Rooms: map[string]json.RawMessage{
				update.RoomID(): ev,
			},
		}
	}
	return nil
}

func (r *TypingRequest) Process(ctx context.Context, res *Response, extCtx Context) {
	if extCtx.Update != nil {
		tres := r.ProcessLiveTyping(extCtx)
		if tres != nil {
			res.Typing = tres // TODO aggregate
		}
		return
	}
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
