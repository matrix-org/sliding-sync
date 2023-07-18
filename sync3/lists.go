package sync3

import (
	"context"
	"strings"

	"github.com/matrix-org/sliding-sync/internal"
)

type OverwriteVal bool

var (
	DoNotOverwrite OverwriteVal = false
	Overwrite      OverwriteVal = true
)

// ListOp represents the possible operations on a list
type ListOp uint8

var (
	// The room is added to the list
	ListOpAdd ListOp = 1
	// The room is removed from the list
	ListOpDel ListOp = 2
	// The room may change position in the list
	ListOpChange ListOp = 3
)

type RoomListDelta struct {
	ListKey string
	Op      ListOp
}

type RoomDelta struct {
	RoomNameChanged          bool
	RoomAvatarChanged        bool
	JoinCountChanged         bool
	InviteCountChanged       bool
	NotificationCountChanged bool
	HighlightCountChanged    bool
	Lists                    []RoomListDelta
}

// InternalRequestLists is a list of lists which matches each index position in the request
// JSON 'lists'. It contains all the internal metadata for rooms and controls access and updatings of said
// lists.
type InternalRequestLists struct {
	allRooms map[string]*RoomConnMetadata
	lists    map[string]*FilteredSortableRooms
}

func NewInternalRequestLists() *InternalRequestLists {
	return &InternalRequestLists{
		allRooms: make(map[string]*RoomConnMetadata, 10),
		lists:    make(map[string]*FilteredSortableRooms),
	}
}

func (s *InternalRequestLists) SetRoom(r RoomConnMetadata) (delta RoomDelta) {
	existing, exists := s.allRooms[r.RoomID]
	if exists {
		if existing.NotificationCount != r.NotificationCount {
			delta.NotificationCountChanged = true
		}
		if existing.HighlightCount != r.HighlightCount {
			delta.HighlightCountChanged = true
		}
		delta.InviteCountChanged = !existing.SameInviteCount(&r.RoomMetadata)
		delta.JoinCountChanged = !existing.SameJoinCount(&r.RoomMetadata)
		delta.RoomNameChanged = !existing.SameRoomName(&r.RoomMetadata)
		if delta.RoomNameChanged {
			// update the canonical name to allow room name sorting to continue to work
			r.CanonicalisedName = strings.ToLower(
				strings.Trim(internal.CalculateRoomName(&r.RoomMetadata, 5), "#!():_@"),
			)
		} else {
			// XXX: during TestConnectionTimeoutNotReset there is some situation where
			//      r.CanonicalisedName is the empty string. Looking at the SetRoom
			//      call in connstate_live.go, this is because the UserRoomMetadata on
			//      the RoomUpdate has an empty CanonicalisedName. Either
			//        a) that is expected, in which case we should _always_ write to
			//           r.CanonicalisedName here; or
			//        b) that is not expected, in which case... erm, I don't know what
			//           to conclude.
			r.CanonicalisedName = existing.CanonicalisedName
		}
		delta.RoomAvatarChanged = !existing.SameRoomAvatar(&r.RoomMetadata)
		if delta.RoomAvatarChanged {
			r.ResolvedAvatarURL = internal.CalculateAvatar(&r.RoomMetadata)
		}

		// Interpret the timestamp map on r as the changes we should apply atop the
		// existing timestamps.
		newTimestamps := r.LastInterestedEventTimestamps
		r.LastInterestedEventTimestamps = make(map[string]uint64, len(s.lists))
		for listKey := range s.lists {
			newTs, bump := newTimestamps[listKey]
			if bump {
				r.LastInterestedEventTimestamps[listKey] = newTs
			} else {
				prevTs, hadPreviousTs := existing.LastInterestedEventTimestamps[listKey]
				if hadPreviousTs {
					r.LastInterestedEventTimestamps[listKey] = prevTs
				} else {
					// This can happen if the listKey is brand-new in this request.
					r.LastInterestedEventTimestamps[listKey] = existing.LastMessageTimestamp
				}
			}
		}
	} else {
		// set the canonical name to allow room name sorting to work
		r.CanonicalisedName = strings.ToLower(
			strings.Trim(internal.CalculateRoomName(&r.RoomMetadata, 5), "#!():_@"),
		)
		r.ResolvedAvatarURL = internal.CalculateAvatar(&r.RoomMetadata)
		// We'll automatically use the LastInterestedEventTimestamps provided by the
		// caller, so that recency sorts work.
	}
	// filter.Include may call on this room ID in the RoomFinder, so make sure it finds it.
	s.allRooms[r.RoomID] = &r

	for listKey, list := range s.lists {
		_, alreadyExists := list.roomIDToIndex[r.RoomID]
		shouldExist := list.filter.Include(&r, s)
		if shouldExist && r.HasLeft {
			shouldExist = false
		}
		// weird nesting ensures we handle all 4 cases
		if alreadyExists {
			if shouldExist { // could be a change
				delta.Lists = append(delta.Lists, RoomListDelta{
					ListKey: listKey,
					Op:      ListOpChange,
				})
			} else { // removal
				delta.Lists = append(delta.Lists, RoomListDelta{
					ListKey: listKey,
					Op:      ListOpDel,
				})
			}
		} else {
			if shouldExist { // addition
				delta.Lists = append(delta.Lists, RoomListDelta{
					ListKey: listKey,
					Op:      ListOpAdd,
				})
			} // else it doesn't exist and it shouldn't exist, so do nothing e.g room isn't relevant to this list
		}
	}
	return delta
}

// Remove a room from all lists e.g retired an invite, left a room
func (s *InternalRequestLists) RemoveRoom(roomID string) {
	delete(s.allRooms, roomID)
	// TODO: update lists?
}

func (s *InternalRequestLists) DeleteList(listKey string) {
	delete(s.lists, listKey)
	for _, room := range s.allRooms {
		delete(room.LastInterestedEventTimestamps, listKey)
	}
}

// Returns the underlying RoomConnMetadata object. Returns a shared pointer, not a copy.
// It is only safe to read this data, never to write.
func (s *InternalRequestLists) ReadOnlyRoom(roomID string) *RoomConnMetadata {
	return s.allRooms[roomID]
}

// Get returns the sorted list of rooms. Returns a shared pointer, not a copy.
// It is only safe to read this data, never to write.
func (s *InternalRequestLists) Get(listKey string) *FilteredSortableRooms {
	return s.lists[listKey]
}

// ListKeys returns a copy of the list keys currently tracked by this
// InternalRequestLists struct, in no particular order. Outside of test code, you
// probably don't want to call this---you probably have the set of list keys tracked
// elsewhere in the application.
func (s *InternalRequestLists) ListKeys() []string {
	keys := make([]string, len(s.lists))
	for listKey, _ := range s.lists {
		keys = append(keys, listKey)
	}
	return keys
}

// ListsByVisibleRoomIDs builds a map from room IDs to a slice of list names. Keys are
// all room IDs that are currently visible in at least one sliding window. Values are
// the names of all lists (in no particular order) in which the given room ID is
// currently visible. The value slices are nonnil and contain at least one list name
// (possibly more).
//
// The returned map is a copy, i.e. is safe to modify by the caller.
func (s *InternalRequestLists) ListsByVisibleRoomIDs(muxedReqLists map[string]RequestList) map[string][]string {
	listsByRoomIDs := make(map[string][]string, len(muxedReqLists))
	// Loop over each list, and mark each room in its sliding window as being visible in this list.
	for listKey, reqList := range muxedReqLists {
		sortedRooms := s.lists[listKey].SortableRooms
		if sortedRooms == nil {
			continue
		}

		// If we've requested all rooms, every room is visible in this list---we don't
		// have to worry about extracting room IDs in the sliding windows' ranges.
		if reqList.SlowGetAllRooms != nil && *reqList.SlowGetAllRooms {
			for _, roomID := range sortedRooms.RoomIDs() {
				listsByRoomIDs[roomID] = append(listsByRoomIDs[roomID], listKey)
			}
		} else {
			subslices := reqList.Ranges.SliceInto(sortedRooms)
			for _, subslice := range subslices {
				sortedRooms = subslice.(*SortableRooms)
				for _, roomID := range sortedRooms.RoomIDs() {
					listsByRoomIDs[roomID] = append(listsByRoomIDs[roomID], listKey)
				}
			}
		}
	}
	return listsByRoomIDs
}

// Assign a new list at the given key. If Overwrite, any existing list is replaced. If DoNotOverwrite, the existing
// list is returned if one exists, else a new list is created. Returns the list and true if the list was overwritten.
func (s *InternalRequestLists) AssignList(ctx context.Context, listKey string, filters *RequestFilters, sort []string, shouldOverwrite OverwriteVal) (*FilteredSortableRooms, bool) {
	if shouldOverwrite == DoNotOverwrite {
		_, exists := s.lists[listKey]
		if exists {
			return s.lists[listKey], false
		}
	}
	roomIDs := make([]string, len(s.allRooms))
	i := 0
	for roomID := range s.allRooms {
		roomIDs[i] = roomID
		i++
	}

	roomList := NewFilteredSortableRooms(s, listKey, roomIDs, filters)
	if sort != nil {
		err := roomList.Sort(sort)
		if err != nil {
			logger.Err(err).Strs("sort_by", sort).Msg("failed to sort")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		}
	}
	s.lists[listKey] = roomList
	return roomList, true
}

// Count returns the count of total rooms in this list
func (s *InternalRequestLists) Count(listKey string) int {
	return int(s.lists[listKey].Len())
}

func (s *InternalRequestLists) Len() int {
	return len(s.lists)
}
