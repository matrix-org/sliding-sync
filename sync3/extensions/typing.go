package extensions

import (
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

func ProcessLiveTyping(up caches.Update, updateWillReturnResponse bool, userID string, req *TypingRequest) (res *TypingResponse) {
	switch update := up.(type) {
	case *caches.TypingUpdate:
		// a live typing event happened, send this back
		return &TypingResponse{
			Rooms: map[string]json.RawMessage{
				update.RoomID(): update.GlobalRoomMetadata().TypingEvent,
			},
		}
	case caches.RoomUpdate:
		// this is a room update which is causing us to return, meaning we are interested in this room.
		// send typing for this room.
		if !updateWillReturnResponse {
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

func ProcessTyping(globalCache *caches.GlobalCache, roomIDToTimeline map[string][]string, userID string, isInitial bool, req *TypingRequest) (res *TypingResponse) {
	// grab typing users for all the rooms we're going to return
	res = &TypingResponse{
		Rooms: make(map[string]json.RawMessage),
	}
	roomIDs := make([]string, 0, len(roomIDToTimeline))
	for roomID := range roomIDToTimeline {
		roomIDs = append(roomIDs, roomID)
	}
	roomToGlobalMetadata := globalCache.LoadRooms(roomIDs...)
	for roomID := range roomIDToTimeline {
		meta := roomToGlobalMetadata[roomID]
		if meta == nil || meta.TypingEvent == nil {
			continue
		}
		res.Rooms[roomID] = meta.TypingEvent
	}
	return
}
