package sync3

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sync3/extensions"
)

var (
	SortByName              = "by_name"
	SortByRecency           = "by_recency"
	SortByNotificationCount = "by_notification_count"
	SortByHighlightCount    = "by_highlight_count"
	SortBy                  = []string{SortByHighlightCount, SortByName, SortByNotificationCount, SortByRecency}

	DefaultTimelineLimit = int64(20)
	DefaultTimeoutMSecs  = 10 * 1000 // 10s
)

type Request struct {
	Lists             []RequestList               `json:"lists"`
	RoomSubscriptions map[string]RoomSubscription `json:"room_subscriptions"`
	UnsubscribeRooms  []string                    `json:"unsubscribe_rooms"`
	Extensions        extensions.Request          `json:"extensions"`

	// set via query params or inferred
	pos          int64
	timeoutMSecs int
}

type RequestList struct {
	RoomSubscription
	Ranges  SliceRanges     `json:"ranges"`
	Sort    []string        `json:"sort"`
	Filters *RequestFilters `json:"filters"`
}

func (r *Request) SetPos(pos int64) {
	r.pos = pos
}
func (r *Request) TimeoutMSecs() int {
	return r.timeoutMSecs
}
func (r *Request) SetTimeoutMSecs(timeout int) {
	r.timeoutMSecs = timeout
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
	result = &Request{
		Extensions: r.Extensions.ApplyDelta(&nextReq.Extensions),
	}
	lists := make([]RequestList, len(nextReq.Lists))
	for i := 0; i < len(lists); i++ {
		var existingList *RequestList
		if i < len(r.Lists) {
			existingList = &r.Lists[i]
		}
		if existingList == nil {
			// we added a list
			lists[i] = nextReq.Lists[i]
			continue
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
			RoomSubscription: RoomSubscription{
				RequiredState: reqState,
				TimelineLimit: timelineLimit,
			},
			Ranges:  rooms,
			Sort:    sort,
			Filters: filters,
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

func (r *Request) GetRequiredStateForRoom(roomID string) [][2]string {
	if r.RoomSubscriptions == nil || roomID == "" {
		return nil
	}
	room, ok := r.RoomSubscriptions[roomID]
	if ok {
		if room.RequiredState != nil {
			return room.RequiredState
		}
	}
	return nil
}

type RequestFilters struct {
	Spaces         []string `json:"spaces"`
	IsDM           *bool    `json:"is_dm"`
	IsEncrypted    *bool    `json:"is_encrypted"`
	IsInvite       *bool    `json:"is_invite"`
	IsTombstoned   *bool    `json:"is_tombstoned"`
	RoomNameFilter string   `json:"room_name_like"`
	// TODO options to control which events should be live-streamed e.g not_types, types from sync v2
}

func (rf *RequestFilters) Include(r *RoomConnMetadata) bool {
	if rf.IsEncrypted != nil && *rf.IsEncrypted != r.Encrypted {
		return false
	}
	if rf.IsTombstoned != nil && *rf.IsTombstoned != r.Tombstoned {
		return false
	}
	if rf.IsDM != nil && *rf.IsDM != r.IsDM {
		return false
	}
	if rf.IsInvite != nil && *rf.IsInvite != r.IsInvite {
		return false
	}
	if rf.RoomNameFilter != "" && !strings.Contains(strings.ToLower(internal.CalculateRoomName(&r.RoomMetadata, 5)), strings.ToLower(rf.RoomNameFilter)) {
		return false
	}
	return true
}

func ChangedFilters(prev, next *RequestFilters) bool {
	// easier to marshal as JSON rather than do a bazillion nil checks
	pb, err := json.Marshal(prev)
	if err != nil {
		panic(err)
	}
	nb, err := json.Marshal(next)
	if err != nil {
		panic(err)
	}
	return !bytes.Equal(pb, nb)
}

type RoomSubscription struct {
	RequiredState [][2]string `json:"required_state"`
	TimelineLimit int64       `json:"timeline_limit"`
}

// Combine this subcription with another, returning a union of both as a copy.
func (rs RoomSubscription) Combine(other RoomSubscription) RoomSubscription {
	var result RoomSubscription
	// choose max value
	if rs.TimelineLimit > other.TimelineLimit {
		result.TimelineLimit = rs.TimelineLimit
	} else {
		result.TimelineLimit = other.TimelineLimit
	}
	// combine together required_state fields, we'll union them later
	result.RequiredState = append(rs.RequiredState, other.RequiredState...)
	return result
}

// Calculate the required state map for this room subscription. Given event types A,B,C and state keys
// 1,2,3, the following Venn diagrams are possible:
//  .---------[*,*]----------.
//  |      .---------.       |
//  |      |   A,2   | A,3   |
//  | .----+--[B,*]--+-----. |
//  | |    | .-----. |     | |
//  | |B,1 | | B,2 | | B,3 | |
//  | |    | `[B,2]` |     | |
//  | `----+---------+-----` |
//  |      |   C,2   | C,3   |
//  |      `--[*,2]--`       |
//  `------------------------`
//
// The largest set will be used when returning the required state map.
// For example, [B,2] + [B,*] = [B,*] because [B,*] encompasses [B,2]. This means [*,*] encompasses
// everything.
func (rs RoomSubscription) RequiredStateMap() *RequiredStateMap {
	result := make(map[string][]string)
	eventTypesWithWildcardStateKeys := make(map[string]struct{})
	var stateKeysForWildcardEventType []string
	for _, tuple := range rs.RequiredState {
		if tuple[0] == "*" {
			if tuple[1] == "*" { // all state
				return &RequiredStateMap{
					allState: true,
				}
			}
			stateKeysForWildcardEventType = append(stateKeysForWildcardEventType, tuple[1])
			continue
		}
		if tuple[1] == "*" { // wildcard state key
			eventTypesWithWildcardStateKeys[tuple[0]] = struct{}{}
		} else {
			result[tuple[0]] = append(result[tuple[0]], tuple[1])
		}
	}
	return &RequiredStateMap{
		eventTypesWithWildcardStateKeys: eventTypesWithWildcardStateKeys,
		stateKeysForWildcardEventType:   stateKeysForWildcardEventType,
		eventTypeToStateKeys:            result,
		allState:                        false,
	}
}

type RequiredStateMap struct {
	eventTypesWithWildcardStateKeys map[string]struct{}
	stateKeysForWildcardEventType   []string
	eventTypeToStateKeys            map[string][]string
	allState                        bool
}

func (rsm *RequiredStateMap) Include(evType, stateKey string) bool {
	if rsm.allState {
		return true
	}
	// check if we should include this event due to wildcard event types
	for _, sk := range rsm.stateKeysForWildcardEventType {
		if sk == stateKey || sk == "*" {
			return true
		}
	}
	// check if we should include this event due to wildcard state keys
	for et := range rsm.eventTypesWithWildcardStateKeys {
		if et == evType {
			return true
		}
	}
	// check if we should include this event due to exact type/state key match
	for _, sk := range rsm.eventTypeToStateKeys[evType] {
		if sk == stateKey {
			return true
		}
	}
	return false
}

// work out what to ask the storage layer: if we have wildcard event types we need to pull all
// room state and cannot only pull out certain event types. If we have wildcard state keys we
// need to use an empty list for state keys.
func (rsm *RequiredStateMap) QueryStateMap() map[string][]string {
	queryStateMap := make(map[string][]string)
	if len(rsm.stateKeysForWildcardEventType) == 0 { // no wildcard event types
		for evType, stateKeys := range rsm.eventTypeToStateKeys {
			queryStateMap[evType] = stateKeys
		}
		for evType := range rsm.eventTypesWithWildcardStateKeys {
			queryStateMap[evType] = nil
		}
	}
	return queryStateMap
}
