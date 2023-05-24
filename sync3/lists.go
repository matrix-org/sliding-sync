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

func (s *InternalRequestLists) SetRoom(r RoomConnMetadata, replacePreviousTimestamp bool) (delta RoomDelta) {
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
		}

		// Only bump this room in the room list if the update is of interest to the
		// client.
		if replacePreviousTimestamp {
			r.LastInterestedEventTimestamp = r.LastMessageTimestamp
		} else {
			r.LastInterestedEventTimestamp = existing.LastInterestedEventTimestamp
		}
	} else {
		// set the canonical name to allow room name sorting to work
		r.CanonicalisedName = strings.ToLower(
			strings.Trim(internal.CalculateRoomName(&r.RoomMetadata, 5), "#!():_@"),
		)
		// Also set a lastActivityTimestamp so recency sorting works.
		r.LastInterestedEventTimestamp = r.LastMessageTimestamp
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
	// TODO
}

// Returns the underlying Room object. Returns a shared pointer, not a copy.
// It is only safe to read this data, never to write.
func (s *InternalRequestLists) ReadOnlyRoom(roomID string) *RoomConnMetadata {
	return s.allRooms[roomID]
}

func (s *InternalRequestLists) Get(listKey string) *FilteredSortableRooms {
	return s.lists[listKey]
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
	for listName, reqList := range muxedReqLists {
		sortedRooms := s.lists[listName].SortableRooms
		if sortedRooms == nil {
			continue
		}

		// If we've requested all rooms, every room is visible in this list---we don't
		// have to worry about extracting room IDs in the sliding windows' ranges.
		if reqList.SlowGetAllRooms != nil && *reqList.SlowGetAllRooms {
			for _, roomID := range sortedRooms.RoomIDs() {
				listsByRoomIDs[roomID] = append(listsByRoomIDs[roomID], listName)
			}
		} else {
			subslices := reqList.Ranges.SliceInto(sortedRooms)
			for _, subslice := range subslices {
				sortedRooms = subslice.(*SortableRooms)
				for _, roomID := range sortedRooms.RoomIDs() {
					listsByRoomIDs[roomID] = append(listsByRoomIDs[roomID], listName)
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
