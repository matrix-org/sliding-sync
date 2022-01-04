package sync3

import (
	"bytes"
	"encoding/json"

	"github.com/matrix-org/sync-v3/sync3/extensions"
)

var (
	SortByName              = "by_name"
	SortByRecency           = "by_recency"
	SortByNotificationCount = "by_notification_count"
	SortByHighlightCount    = "by_highlight_count"
	SortBy                  = []string{SortByHighlightCount, SortByName, SortByNotificationCount, SortByRecency}

	DefaultTimelineLimit = int64(20)
	DefaultTimeoutSecs   = 10
)

type Request struct {
	Lists             []RequestList               `json:"lists"`
	RoomSubscriptions map[string]RoomSubscription `json:"room_subscriptions"`
	UnsubscribeRooms  []string                    `json:"unsubscribe_rooms"`
	Extensions        extensions.Request          `json:"extensions"`

	// set via query params or inferred
	pos         int64
	timeoutSecs int
	SessionID   string `json:"session_id"`
}

type RequestList struct {
	Ranges        SliceRanges     `json:"ranges"`
	Sort          []string        `json:"sort"`
	RequiredState [][2]string     `json:"required_state"`
	TimelineLimit int64           `json:"timeline_limit"`
	Filters       *RequestFilters `json:"filters"`
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
func (r *Request) ApplyDelta(nextReq *Request) (result *Request, subs, unsubs []string) {
	// Use the newer values unless they aren't specified, then use the older ones.
	// Go is ew in that this can't be represented in a nicer way
	sessionID := nextReq.SessionID
	if sessionID == "" {
		sessionID = r.SessionID
	}
	result = &Request{
		SessionID:  sessionID,
		Extensions: nextReq.Extensions, // TODO: make them sticky
	}
	lists := make([]RequestList, len(nextReq.Lists))
	for i := 0; i < len(lists); i++ {
		var existingList *RequestList
		if i < len(r.Lists) {
			existingList = &r.Lists[i]
		}
		nextList := nextReq.Lists[i]
		rooms := nextList.Ranges
		if rooms == nil {
			rooms = existingList.Ranges
		}
		sort := nextList.Sort
		if sort == nil {
			sort = existingList.Sort
		}
		reqState := nextList.RequiredState
		if reqState == nil {
			reqState = existingList.RequiredState
		}
		timelineLimit := nextList.TimelineLimit
		if timelineLimit == 0 {
			timelineLimit = existingList.TimelineLimit
		}
		filters := nextList.Filters
		if filters == nil {
			filters = existingList.Filters
		}
		lists[i] = RequestList{
			Ranges:        rooms,
			Sort:          sort,
			RequiredState: reqState,
			TimelineLimit: timelineLimit,
			Filters:       filters,
		}
	}
	result.Lists = lists
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
	for roomID, val := range nextReq.RoomSubscriptions {
		// either updating an existing sub or is a new sub, we don't care which for now.
		resultSubs[roomID] = val
	}
	for _, roomID := range nextReq.UnsubscribeRooms {
		_, ok := resultSubs[roomID]
		if ok {
			// if this request both subscribes and unsubscribes to the same room ID,
			// don't mark this as an unsub delta
			if _, ok = nextReq.RoomSubscriptions[roomID]; !ok {
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

func (r *Request) GetTimelineLimit(listIndex int, roomID string) int64 {
	if r.RoomSubscriptions != nil {
		room, ok := r.RoomSubscriptions[roomID]
		if ok && room.TimelineLimit > 0 {
			return room.TimelineLimit
		}
	}
	if r.Lists[listIndex].TimelineLimit > 0 {
		return r.Lists[listIndex].TimelineLimit
	}
	return DefaultTimelineLimit
}

func (r *Request) GetRequiredState(listIndex int, roomID string) [][2]string {
	if r.RoomSubscriptions != nil {
		room, ok := r.RoomSubscriptions[roomID]
		if ok && room.RequiredState != nil {
			return room.RequiredState
		}
	}
	return r.Lists[listIndex].RequiredState
}

type RequestFilters struct {
	Spaces      []string `json:"spaces"`
	IsDM        *bool    `json:"is_dm"`
	IsEncrypted *bool    `json:"is_encrypted"`
	IsInvite    *bool    `json:"is_invite"`
	// TODO options to control which events should be live-streamed e.g not_types, types from sync v2
}

func (rf *RequestFilters) Include(r *RoomConnMetadata) bool {
	if rf.IsEncrypted != nil && *rf.IsEncrypted != r.Encrypted {
		return false
	}
	if rf.IsDM != nil && *rf.IsDM != r.IsDM {
		return false
	}
	if rf.IsInvite != nil && *rf.IsInvite != r.IsInvite {
		return false
	}
	return true
}

type RoomSubscription struct {
	RequiredState [][2]string `json:"required_state"`
	TimelineLimit int64       `json:"timeline_limit"`
}
