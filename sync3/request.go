package sync3

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
)

var (
	SortByName              = "by_name"
	SortByRecency           = "by_recency"
	SortByNotificationLevel = "by_notification_level"
	SortByNotificationCount = "by_notification_count" // deprecated
	SortByHighlightCount    = "by_highlight_count"    // deprecated
	SortBy                  = []string{SortByHighlightCount, SortByName, SortByNotificationCount, SortByRecency, SortByNotificationLevel}

	Wildcard     = "*"
	StateKeyLazy = "$LAZY"
	StateKeyMe   = "$ME"

	DefaultTimelineLimit = int64(20)
	DefaultTimeoutMSecs  = 10 * 1000 // 10s
)

type Request struct {
	TxnID             string                      `json:"txn_id"`
	ConnID            string                      `json:"conn_id"`
	Lists             map[string]RequestList      `json:"lists"`
	BumpEventTypes    []string                    `json:"bump_event_types"`
	RoomSubscriptions map[string]RoomSubscription `json:"room_subscriptions"`
	UnsubscribeRooms  []string                    `json:"unsubscribe_rooms"`
	Extensions        extensions.Request          `json:"extensions"`

	// set via query params or inferred
	pos          int64
	timeoutMSecs int
}

func (r *Request) Validate() error {
	if len(r.ConnID) > 16 {
		return fmt.Errorf("conn_id is too long: %d > 16", len(r.ConnID))
	}
	if len(r.TxnID) > 64 {
		return fmt.Errorf("txn_id is too long: %d > 64", len(r.TxnID))
	}
	return nil
}

type RequestList struct {
	RoomSubscription
	Ranges          SliceRanges     `json:"ranges"`
	Sort            []string        `json:"sort"`
	Filters         *RequestFilters `json:"filters"`
	SlowGetAllRooms *bool           `json:"slow_get_all_rooms,omitempty"`
	Deleted         bool            `json:"deleted,omitempty"`
}

func (rl *RequestList) ShouldGetAllRooms() bool {
	return rl.SlowGetAllRooms != nil && *rl.SlowGetAllRooms
}

func (rl *RequestList) SortOrderChanged(next *RequestList) bool {
	prevLen := 0
	if rl != nil {
		prevLen = len(rl.Sort)
	}
	if prevLen != len(next.Sort) {
		return true
	}
	for i := range rl.Sort {
		if rl.Sort[i] != next.Sort[i] {
			return true
		}
	}
	return false
}

func (rl *RequestList) TimelineLimitChanged(next *RequestList) bool {
	limit := 0
	if rl != nil {
		limit = int(rl.TimelineLimit)
	}
	return limit != int(next.TimelineLimit)
}

func (rl *RequestList) FiltersChanged(next *RequestList) bool {
	var prev *RequestFilters
	if rl != nil {
		prev = rl.Filters
	}
	// easier to marshal as JSON rather than do a bazillion nil checks
	pb, err := json.Marshal(prev)
	if err != nil {
		panic(err)
	}
	nb, err := json.Marshal(next.Filters)
	if err != nil {
		panic(err)
	}
	return !bytes.Equal(pb, nb)
}

// Write an insert operation for this list. Can return nil for indexes not being tracked. Useful when
// rooms are added to the list e.g newly joined rooms.
func (rl *RequestList) WriteInsertOp(insertedIndex int, roomID string) *ResponseOpSingle {
	if insertedIndex < 0 {
		return nil
	}
	// only notify if we are tracking this index
	if _, inside := rl.Ranges.Inside(int64(insertedIndex)); !inside {
		return nil
	}
	return &ResponseOpSingle{
		Operation: OpInsert,
		Index:     &insertedIndex,
		RoomID:    roomID,
	}
}

// Write a delete operation for this list. Can return nil for invalid indexes or if this index isn't being tracked.
// Useful when rooms are removed from the list e.g left rooms.
func (rl *RequestList) WriteDeleteOp(deletedIndex int) *ResponseOpSingle {
	// update operations return -1 if nothing gets deleted
	if deletedIndex < 0 {
		return nil
	}
	// only notify if we are tracking this index
	if _, inside := rl.Ranges.Inside(int64(deletedIndex)); !inside {
		return nil
	}
	return &ResponseOpSingle{
		Operation: OpDelete,
		Index:     &deletedIndex,
	}
}

// Calculate the real from -> to index positions for the two input index positions. This takes into
// account the ranges on the list.
func (rl *RequestList) CalculateMoveIndexes(fromIndex, toIndex int) (fromTos [][2]int) {
	// Given a range like the following there are several cases to consider:
	// 0  1  2  3  4  5  6  7  8  9  10
	//    |--------|        |-----|        [1,4],[7,9]                                           RESULT
	//       T  F                          move inside the same range                             3, 2
	//          T     F                    move from outside to inside the range                  4, 3
	//                F        T           move from outside to inside the range (higher)         7, 8
	//          F        T                 move from inside to outside the range (higher)         3, 4
	//                T        F           move from inside to outside the range                  8, 7
	//          T              F           move between two ranges                                8, 3
	//                F  T                 move outside the ranges, no jumps                      !ok
	// T              F                    move outside the ranges, jumping over a range          4, 1 (everything shift rights)
	// T                              F    move outside the ranges, jumping over multiple ranges  4,1 + 9,7 (everything shifts right)
	//          T                     F    move into range, jumping over a range                  4,3 + 9,7

	// This can be summarised with the following rules:
	//  A- If BOTH from/to are inside the same range: return those indexes.
	//  B- If ONE index is inside a range:
	//     * Use the index inside the range
	//     * Find the direction of movement (towards / away from zero)
	//     * Find the closest range boundary in that direction for the index outside the range and use that.
	//     * Check if jumped over any ranges, if so then set from/to index to the range boundaries according to the direction of movement
	//     * Return potentially > 1 set of move indexes
	//  C- If BOTH from/to are outside ranges:
	//     * Find which ranges are jumped over. If none, return !ok
	//     * For each jumped over range:
	//        * Set from/to index to the range boundaries according to the direction of movement
	//     * Return potentially > 1 set of move indexes

	fromRng, isFromInsideRange := rl.Ranges.Inside(int64(fromIndex))
	toRng, isToInsideRange := rl.Ranges.Inside(int64(toIndex))
	if isFromInsideRange && isToInsideRange && fromRng == toRng { // case A
		return [][2]int{{fromIndex, toIndex}}
	}
	if !isFromInsideRange && !isToInsideRange { // case C
		// jumping over multiple range
		// work out which ranges are jumped over
		jumpedOverRanges := rl.jumpedOverRanges(fromIndex, toIndex)
		if len(jumpedOverRanges) == 0 {
			return nil
		}
		// handle multiple ranges
		for _, jumpedOverRange := range jumpedOverRanges {
			if fromIndex > toIndex { // heading towards zero
				fromTos = append(fromTos, [2]int{int(jumpedOverRange[1]), int(jumpedOverRange[0])})
			} else {
				fromTos = append(fromTos, [2]int{int(jumpedOverRange[0]), int(jumpedOverRange[1])})
			}
		}
		return fromTos
	}

	// case B
	if isFromInsideRange {
		// snap toIndex to a lower value i.e towards zero IF to > from
		fromTos = append(fromTos, [2]int{
			fromIndex, int(rl.Ranges.ClosestInDirection(int64(fromIndex), toIndex < fromIndex)),
		})
	}
	if isToInsideRange {
		// snap fromIndex to either the upper/lower range depending on the direction of travel:
		// if from > to then we want the upper range, if from < to we want the lower range.
		fromTos = append(fromTos, [2]int{
			int(rl.Ranges.ClosestInDirection(int64(toIndex), fromIndex < toIndex)), toIndex,
		})
	}
	// check for jumped over ranges
	jumpedOverRanges := rl.jumpedOverRanges(fromIndex, toIndex)
	for _, jumpedOverRange := range jumpedOverRanges {
		if fromIndex > toIndex { // heading towards zero
			fromTos = append(fromTos, [2]int{int(jumpedOverRange[1]), int(jumpedOverRange[0])})
		} else {
			fromTos = append(fromTos, [2]int{int(jumpedOverRange[0]), int(jumpedOverRange[1])})
		}
	}

	return fromTos
}

func (rl *RequestList) jumpedOverRanges(fromIndex, toIndex int) (jumpedOverRanges [][2]int64) {
	hi := int64(fromIndex)
	lo := int64(toIndex)
	if fromIndex < toIndex {
		hi = int64(toIndex)
		lo = int64(fromIndex)
	}
	for _, r := range rl.Ranges {
		if r[0] > lo && r[0] < hi && r[1] > lo && r[1] < hi {
			jumpedOverRanges = append(jumpedOverRanges, r)
		}
	}
	return
}

// Move a room from an absolute index position to another absolute position. These positions do not
// need to be inside a valid range. Returns 0-2 operations. For example:
//
//	1,2,3,4,5 tracking range [0,4]
//	3 bumps to top -> 3,1,2,4,5 -> DELETE index=2, INSERT val=3 index=0
//	7 bumps to top -> 7,1,2,3,4 -> DELETE index=4, INSERT val=7 index=0
//	7 bumps to op again -> 7,1,2,3,4 -> no-op as from == to index
//	new room 8 in i=5 -> 7,1,2,3,4,8 -> no-op as 8 is outside the range.
//
// Returns the list of ops as well as the new toIndex if it wasn't inside a range.
func (rl *RequestList) WriteSwapOp(
	roomID string, fromIndex, toIndex int,
) []ResponseOp {
	return []ResponseOp{
		&ResponseOpSingle{
			Operation: OpDelete,
			Index:     &fromIndex,
		},
		&ResponseOpSingle{
			Operation: OpInsert,
			Index:     &toIndex,
			RoomID:    roomID,
		},
	}
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

// Internal struct used to represent the diffs between 2 requests
type RequestDelta struct {
	// new room IDs to subscribe to
	Subs []string
	// room IDs to unsubscribe from
	Unsubs []string
	// The complete union of both lists (contains max(a,b) lists)
	Lists map[string]RequestListDelta
}

// Internal struct used to represent a single list delta.
type RequestListDelta struct {
	// What was there before, nullable
	Prev *RequestList
	// What is there now, nullable. Combined result.
	Curr *RequestList
}

// Apply this delta on top of the request. Returns a new Request with the combined output, along
// with the delta operations `nextReq` cannot be nil, but `r` can be nil in the case of an initial
// request.
func (r *Request) ApplyDelta(nextReq *Request) (result *Request, delta *RequestDelta) {
	if r == nil {
		result = &Request{
			Extensions: nextReq.Extensions,
		}
		r = &Request{}
	} else {
		// Use the newer values unless they aren't specified, then use the older ones.
		// Go is ew in that this can't be represented in a nicer way
		result = &Request{
			Extensions: r.Extensions.ApplyDelta(&nextReq.Extensions),
		}
	}
	// conn ID isn't sticky, always use the nextReq value. This is only useful for logging,
	// as the conn ID is used primarily in conn_map.go
	result.ConnID = nextReq.ConnID

	listKeys := make(set)
	for k := range nextReq.Lists {
		listKeys[k] = struct{}{}
	}
	for k := range r.Lists {
		listKeys[k] = struct{}{}
	}
	delta = &RequestDelta{}
	calculatedLists := make(map[string]RequestList, len(nextReq.Lists))
	for listKey := range listKeys {
		existingList, existingOk := r.Lists[listKey]
		nextList, nextOk := nextReq.Lists[listKey]
		if !nextOk {
			// copy over what they said before (sticky), no diffs to make
			calculatedLists[listKey] = existingList
			continue
		}
		if !existingOk {
			// we added a list
			// default to recency sort order if missing and there isn't a previous list value to draw from
			if len(nextList.Sort) == 0 {
				nextList.Sort = []string{SortByRecency}
			}
			calculatedLists[listKey] = nextList
			continue
		}
		// both existing and next exist, check for deletions
		if nextList.Deleted {
			// do not add the list to `lists` so it disappears
			continue
		}

		// apply the delta
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
		slowGetAllRooms := nextList.SlowGetAllRooms
		if slowGetAllRooms == nil {
			slowGetAllRooms = existingList.SlowGetAllRooms
		}
		includeOldRooms := nextList.IncludeOldRooms
		if includeOldRooms == nil {
			includeOldRooms = existingList.IncludeOldRooms
		}
		timelineLimit := nextList.TimelineLimit
		if timelineLimit == 0 {
			timelineLimit = existingList.TimelineLimit
		}
		filters := nextList.Filters
		if filters == nil {
			filters = existingList.Filters
		}

		calculatedLists[listKey] = RequestList{
			RoomSubscription: RoomSubscription{
				RequiredState:   reqState,
				TimelineLimit:   timelineLimit,
				IncludeOldRooms: includeOldRooms,
			},
			Ranges:          rooms,
			Sort:            sort,
			Filters:         filters,
			SlowGetAllRooms: slowGetAllRooms,
		}
	}
	result.Lists = calculatedLists

	delta.Lists = make(map[string]RequestListDelta, len(calculatedLists))
	for listKey := range result.Lists {
		l := result.Lists[listKey]
		delta.Lists[listKey] = RequestListDelta{
			Curr: &l,
		}
	}
	for listKey := range r.Lists {
		l := r.Lists[listKey]
		rld := delta.Lists[listKey]
		rld.Prev = &l
		delta.Lists[listKey] = rld
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
			delta.Unsubs = append(delta.Unsubs, roomID)
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
				delta.Unsubs = append(delta.Unsubs, roomID)
			}
		}
		delete(resultSubs, roomID)
	}
	// new subscriptions are the delta between old room subs and the newly calculated ones
	for roomID := range resultSubs {
		if oldSub, ok := r.RoomSubscriptions[roomID]; ok {
			// if the subscription is different, mark it as a delta, else skip it as it hasn't changed
			newSub := resultSubs[roomID]
			if oldSub.RequiredStateChanged(newSub) || oldSub.TimelineLimit != newSub.TimelineLimit {
				delta.Subs = append(delta.Subs, roomID)
			}
			continue // already subscribed
		}
		delta.Subs = append(delta.Subs, roomID)
	}
	result.RoomSubscriptions = resultSubs

	// Make bump_event_types sticky.
	result.BumpEventTypes = nextReq.BumpEventTypes
	if result.BumpEventTypes == nil {
		result.BumpEventTypes = r.BumpEventTypes
	}

	return
}

type RequestFilters struct {
	Spaces         []string  `json:"spaces"`
	IsDM           *bool     `json:"is_dm"`
	IsEncrypted    *bool     `json:"is_encrypted"`
	IsInvite       *bool     `json:"is_invite"`
	IsTombstoned   *bool     `json:"is_tombstoned"` // deprecated
	RoomTypes      []*string `json:"room_types"`
	NotRoomTypes   []*string `json:"not_room_types"`
	RoomNameFilter string    `json:"room_name_like"`
	Tags           []string  `json:"tags"`
	NotTags        []string  `json:"not_tags"`

	// TODO options to control which events should be live-streamed e.g not_types, types from sync v2
}

func (rf *RequestFilters) Include(r *RoomConnMetadata, finder RoomFinder) bool {
	// we always exclude old rooms from lists, but may include them in the `rooms` section if they opt-in
	if r.UpgradedRoomID != nil {
		// should we exclude this room? If we have _joined_ the successor room then yes because
		// this room must therefore be old, else no.
		nextRoom := finder.ReadOnlyRoom(*r.UpgradedRoomID)
		if nextRoom != nil && !nextRoom.HasLeft && !nextRoom.IsInvite {
			return false
		}
	}
	if rf.IsEncrypted != nil && *rf.IsEncrypted != r.Encrypted {
		return false
	}
	if rf.IsTombstoned != nil && *rf.IsTombstoned != (r.UpgradedRoomID != nil) {
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
	if len(rf.NotTags) > 0 {
		for _, t := range rf.NotTags {
			if _, ok := r.Tags[t]; ok {
				return false
			}
		}
	}
	if len(rf.Tags) > 0 {
		tagExists := false
		for _, t := range rf.Tags {
			if _, ok := r.Tags[t]; ok {
				tagExists = true
				break
			}
		}
		if !tagExists {
			return false
		}
	}
	// read not_room_types first as it takes priority
	if nullableStringExists(rf.NotRoomTypes, r.RoomType) {
		return false // explicitly excluded
	}
	if len(rf.RoomTypes) > 0 {
		// either explicitly included or implicitly excluded
		return nullableStringExists(rf.RoomTypes, r.RoomType)
	}
	if len(rf.Spaces) > 0 {
		// ensure this room is a member of one of these spaces
		for _, s := range rf.Spaces {
			if _, ok := r.UserRoomData.Spaces[s]; ok {
				return true
			}
		}
		return false
	}
	return true
}

type RoomSubscription struct {
	RequiredState   [][2]string       `json:"required_state"`
	TimelineLimit   int64             `json:"timeline_limit"`
	IncludeOldRooms *RoomSubscription `json:"include_old_rooms"`
}

func (rs RoomSubscription) RequiredStateChanged(other RoomSubscription) bool {
	if len(rs.RequiredState) != len(other.RequiredState) {
		return true
	}
	for i := range rs.RequiredState {
		if rs.RequiredState[i] != other.RequiredState[i] {
			return true
		}
	}
	return false
}

func (rs RoomSubscription) LazyLoadMembers() bool {
	for _, tuple := range rs.RequiredState {
		if tuple[0] == "m.room.member" && tuple[1] == StateKeyLazy {
			return true
		}
	}
	return false
}

// Combine this subcription with another, returning a union of both as a copy.
func (rs RoomSubscription) Combine(other RoomSubscription) RoomSubscription {
	return rs.combineRecursive(other, true)
}

// Combine this subcription with another, returning a union of both as a copy.
func (rs RoomSubscription) combineRecursive(other RoomSubscription, checkOldRooms bool) RoomSubscription {
	var result RoomSubscription
	// choose max value
	if rs.TimelineLimit > other.TimelineLimit {
		result.TimelineLimit = rs.TimelineLimit
	} else {
		result.TimelineLimit = other.TimelineLimit
	}
	// combine together required_state fields, we'll union them later
	result.RequiredState = append(rs.RequiredState, other.RequiredState...)

	if checkOldRooms {
		// set include_old_rooms if it is unset
		if rs.IncludeOldRooms == nil {
			result.IncludeOldRooms = other.IncludeOldRooms
		} else if other.IncludeOldRooms != nil {
			// 2 subs have include_old_rooms set, union them. Don't check them for old rooms though as that's silly
			ior := rs.IncludeOldRooms.combineRecursive(*other.IncludeOldRooms, false)
			result.IncludeOldRooms = &ior
		}
	}
	return result
}

// Calculate the required state map for this room subscription. Given event types A,B,C and state keys
// 1,2,3, the following Venn diagrams are possible:
//
//	.---------[*,*]----------.
//	|      .---------.       |
//	|      |   A,2   | A,3   |
//	| .----+--[B,*]--+-----. |
//	| |    | .-----. |     | |
//	| |B,1 | | B,2 | | B,3 | |
//	| |    | `[B,2]` |     | |
//	| `----+---------+-----` |
//	|      |   C,2   | C,3   |
//	|      `--[*,2]--`       |
//	`------------------------`
//
// The largest set will be used when returning the required state map.
// For example, [B,2] + [B,*] = [B,*] because [B,*] encompasses [B,2]. This means [*,*] encompasses
// everything.
// 'userID' is the ID of the user performing this request, so $ME can be replaced.
func (rs RoomSubscription) RequiredStateMap(userID string) *internal.RequiredStateMap {
	result := make(map[string][]string)
	eventTypesWithWildcardStateKeys := make(map[string]struct{})
	var stateKeysForWildcardEventType []string
	var allState bool
	for _, tuple := range rs.RequiredState {
		if tuple[1] == StateKeyMe {
			tuple[1] = userID
		}
		if tuple[0] == Wildcard {
			if tuple[1] == Wildcard { // all state
				// we still need to parse required_state as now these filter the result set
				allState = true
				continue
			}
			stateKeysForWildcardEventType = append(stateKeysForWildcardEventType, tuple[1])
			continue
		}
		if tuple[1] == Wildcard { // wildcard state key
			eventTypesWithWildcardStateKeys[tuple[0]] = struct{}{}
		} else {
			result[tuple[0]] = append(result[tuple[0]], tuple[1])
		}
	}
	return internal.NewRequiredStateMap(
		eventTypesWithWildcardStateKeys, stateKeysForWildcardEventType, result, allState, rs.LazyLoadMembers(),
	)
}

// helper to find `null` or literal string matches
func nullableStringExists(arr []*string, input *string) bool {
	if len(arr) == 0 {
		return false
	}
	for _, a := range arr {
		if input == nil {
			if a == nil {
				return true
			}
		} else {
			if a != nil && *a == *input {
				return true
			}
		}
	}
	return false
}
