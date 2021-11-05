package sync3

import (
	"bytes"
	"encoding/json"
)

var (
	SortByName              = "by_name"
	SortByRecency           = "by_recency"
	SortByNotificationCount = "by_notification_count"
	SortByHighlightCount    = "by_highlight_count"
	SortBy                  = []string{SortByHighlightCount, SortByName, SortByNotificationCount, SortByRecency}
	DefaultTimelineLimit    = int64(20)
	DefaultTimeoutSecs      = 10
)

type Request struct {
	Rooms                     SliceRanges                 `json:"rooms"`
	Sort                      []string                    `json:"sort"`
	RequiredState             [][2]string                 `json:"required_state"`
	TimelineLimit             int64                       `json:"timeline_limit"`
	RoomSubscriptions         map[string]RoomSubscription `json:"room_subscriptions"`
	UnsubscribeRooms          []string                    `json:"unsubscribe_rooms"`
	ExcludeEncryptedRoomsFlag *bool                       `json:"exclude_encrypted_rooms"`
	Filters                   *RequestFilters             `json:"filters"`
	// set via query params or inferred
	pos         int64
	timeoutSecs int
	SessionID   string `json:"session_id"`
}

func (r *Request) Pos() int64 {
	return r.pos
}
func (r *Request) SetPos(pos int64) {
	r.pos = pos
}
func (r *Request) TimeoutSecs() int {
	return r.timeoutSecs
}
func (r *Request) SetTimeoutSecs(timeout int) {
	r.timeoutSecs = timeout
}

func (r *Request) Same(other *Request) bool {
	serialised, err := json.Marshal(r)
	if err != nil {
		return false
	}
	otherSer, err := json.Marshal(other)
	if err != nil {
		return false
	}
	return bytes.Equal(serialised, otherSer)
}

// Apply this delta on top of the request. Returns a new Request with the combined output, ready for
// persisting into the database. Also returns the DELTA for rooms to subscribe and unsubscribe from.
func (r *Request) ApplyDelta(next *Request) (result *Request, subs, unsubs []string) {
	// Use the newer values unless they aren't specified, then use the older ones.
	// Go is ew in that this can't be represented in a nicer way
	sessionID := next.SessionID
	if sessionID == "" {
		sessionID = r.SessionID
	}
	rooms := next.Rooms
	if rooms == nil {
		rooms = r.Rooms
	}
	sort := next.Sort
	if sort == nil {
		sort = r.Sort
	}
	globalReqState := next.RequiredState
	if globalReqState == nil {
		globalReqState = r.RequiredState
	}
	timelineLimit := next.TimelineLimit
	if timelineLimit == 0 {
		timelineLimit = r.TimelineLimit
	}
	filters := next.Filters
	if filters == nil {
		filters = r.Filters
	}
	excludeEncryptedRooms := next.ExcludeEncryptedRoomsFlag
	if excludeEncryptedRooms == nil {
		excludeEncryptedRooms = r.ExcludeEncryptedRoomsFlag
	}
	result = &Request{
		SessionID:                 sessionID,
		Rooms:                     rooms,
		Sort:                      sort,
		RequiredState:             globalReqState,
		TimelineLimit:             timelineLimit,
		Filters:                   filters,
		ExcludeEncryptedRoomsFlag: excludeEncryptedRooms,
	}
	// Work out subscriptions. The operations are applied as:
	// old.subs -> apply old.unsubs (should be empty) -> apply new.subs -> apply new.unsubs
	// Meaning if a room is both in subs and unsubs then the result is unsub.
	// This also allows clients to update their filters for an existing room subscription.
	resultSubs := make(map[string]RoomSubscription)
	for roomID, val := range r.RoomSubscriptions {
		resultSubs[roomID] = val
	}
	for _, roomID := range r.UnsubscribeRooms {
		_, ok := resultSubs[roomID]
		if ok {
			unsubs = append(unsubs, roomID)
		}
		delete(resultSubs, roomID)
	}
	for roomID, val := range next.RoomSubscriptions {
		// either updating an existing sub or is a new sub, we don't care which for now.
		resultSubs[roomID] = val
	}
	for _, roomID := range next.UnsubscribeRooms {
		_, ok := resultSubs[roomID]
		if ok {
			// if this request both subscribes and unsubscribes to the same room ID,
			// don't mark this as an unsub delta
			if _, ok = next.RoomSubscriptions[roomID]; !ok {
				unsubs = append(unsubs, roomID)
			}
		}
		delete(resultSubs, roomID)
	}
	// new subscriptions are the delta between old room subs and the newly calculated ones
	for roomID := range resultSubs {
		if _, ok := r.RoomSubscriptions[roomID]; ok {
			continue // already subscribed
		}
		subs = append(subs, roomID)
	}
	result.RoomSubscriptions = resultSubs
	return
}

func (r *Request) GetTimelineLimit(roomID string) int64 {
	limit := DefaultTimelineLimit
	if r.RoomSubscriptions != nil {
		room, ok := r.RoomSubscriptions[roomID]
		if ok && room.TimelineLimit > 0 {
			return room.TimelineLimit
		}
	}
	if r.TimelineLimit > 0 {
		limit = r.TimelineLimit
	}
	return limit
}

func (r *Request) GetRequiredState(roomID string) [][2]string {
	rs := r.RequiredState
	if r.RoomSubscriptions != nil {
		room, ok := r.RoomSubscriptions[roomID]
		if ok && room.RequiredState != nil {
			rs = room.RequiredState
		}
	}
	return rs
}

func (r *Request) ExcludeEncryptedRooms() bool {
	if r.ExcludeEncryptedRoomsFlag == nil {
		// unset -> false
		return false
	}
	return *r.ExcludeEncryptedRoomsFlag
}

type RequestFilters struct {
	Spaces []string `json:"spaces"`
	// TODO options to control which events should be live-streamed e.g not_types, types from sync v2
}

type RoomSubscription struct {
	RequiredState [][2]string `json:"required_state"`
	TimelineLimit int64       `json:"timeline_limit"`
}
