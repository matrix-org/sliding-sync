package extensions

import (
	"context"
	"encoding/json"

	"github.com/matrix-org/sliding-sync/sync3/caches"
)

// Client created request params
type TypingRequest struct {
	Core
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

func (r *TypingRequest) AppendLive(ctx context.Context, res *Response, extCtx Context, up caches.Update) {
	var typingEvent json.RawMessage
	var roomID string
	switch update := up.(type) {
	case *caches.TypingUpdate:
		// a live typing event happened, send this back. Allow for aggregation (>1 typing event in same room => replace)
		roomID = update.RoomID()
		typingEvent = update.GlobalRoomMetadata().TypingEvent
	case caches.RoomUpdate:
		// if this is a room update which is included in the response, send typing notifs for this room
		if _, exists := extCtx.RoomIDToTimeline[update.RoomID()]; !exists {
			return
		}
		ev := update.GlobalRoomMetadata().TypingEvent
		if ev == nil {
			return
		}
		roomID = update.RoomID()
		typingEvent = ev
	}
	if roomID == "" || typingEvent == nil {
		return
	}

	// We've found a typing event. Ignore it if the client doesn't want to know about it.
	if !r.shouldProcessUpdate(roomID, extCtx) {
		return
	}

	if res.Typing == nil {
		res.Typing = &TypingResponse{
			Rooms: make(map[string]json.RawMessage),
		}
	}
	res.Typing.Rooms[roomID] = typingEvent
}

func (r *TypingRequest) shouldProcessUpdate(roomID string, extCtx Context) bool {
	// If the extension hasn't had its scope configured, process everything.
	if r.Lists == nil && r.Rooms == nil {
		return true
	}

	// If this extension has been explicitly subscribed to this room, process the update.
	for _, roomInScope := range r.Rooms {
		if roomInScope == roomID {
			return true
		}
	}

	// If the room belongs to one of the lists that this extension should process, process the update.
	visibleInLists := extCtx.RoomIDsToLists[roomID]
	for _, visibleInList := range visibleInLists {
		for _, shouldProcessList := range r.Lists {
			if visibleInList == shouldProcessList {
				return true
			}
		}
	}

	// Otherwise ignore the update.
	return false
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

		if !r.shouldProcessUpdate(roomID, extCtx) {
			continue
		}

		rooms[roomID] = meta.TypingEvent
	}
	if len(rooms) == 0 {
		return // don't add a typing extension, no data!
	}
	res.Typing = &TypingResponse{
		Rooms: rooms,
	}
}
