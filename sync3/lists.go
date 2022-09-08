package sync3

import (
	"strings"

	"github.com/matrix-org/sync-v3/internal"
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
	ListIndex int
	Op        ListOp
}

type RoomDelta struct {
	RoomNameChanged    bool
	JoinCountChanged   bool
	InviteCountChanged bool
	Lists              []RoomListDelta
}

// InternalRequestLists is a list of lists which matches each index position in the request
// JSON 'lists'. It contains all the internal metadata for rooms and controls access and updatings of said
// lists.
type InternalRequestLists struct {
	allRooms map[string]RoomConnMetadata
	lists    []*FilteredSortableRooms
}

func NewInternalRequestLists() *InternalRequestLists {
	return &InternalRequestLists{
		allRooms: make(map[string]RoomConnMetadata, 10),
	}
}

func (s *InternalRequestLists) SetRoom(r RoomConnMetadata) (delta RoomDelta) {
	existing, exists := s.allRooms[r.RoomID]
	if exists {
		delta.InviteCountChanged = !existing.SameInviteCount(&r.RoomMetadata)
		delta.JoinCountChanged = !existing.SameJoinCount(&r.RoomMetadata)
		delta.RoomNameChanged = !existing.SameRoomName(&r.RoomMetadata)
		if delta.RoomNameChanged {
			// update the canonical name to allow room name sorting to continue to work
			r.CanonicalisedName = strings.ToLower(
				strings.Trim(internal.CalculateRoomName(&r.RoomMetadata, 5), "#!():_@"),
			)
		}
	} else {
		// set the canonical name to allow room name sorting to work
		r.CanonicalisedName = strings.ToLower(
			strings.Trim(internal.CalculateRoomName(&r.RoomMetadata, 5), "#!():_@"),
		)
	}
	for i := range s.lists {
		_, alreadyExists := s.lists[i].roomIDToIndex[r.RoomID]
		shouldExist := s.lists[i].filter.Include(&r)
		if shouldExist && r.HasLeft {
			shouldExist = false
		}
		// weird nesting ensures we handle all 4 cases
		if alreadyExists {
			if shouldExist { // could be a change
				delta.Lists = append(delta.Lists, RoomListDelta{
					ListIndex: i,
					Op:        ListOpChange,
				})
			} else { // removal
				delta.Lists = append(delta.Lists, RoomListDelta{
					ListIndex: i,
					Op:        ListOpDel,
				})
			}
		} else {
			if shouldExist { // addition
				delta.Lists = append(delta.Lists, RoomListDelta{
					ListIndex: i,
					Op:        ListOpAdd,
				})
			} // else it doesn't exist and it shouldn't exist, so do nothing e.g room isn't relevant to this list
		}
	}
	s.allRooms[r.RoomID] = r
	return delta
}

// Remove a room from all lists e.g retired an invite, left a room
func (s *InternalRequestLists) RemoveRoom(roomID string) {
	delete(s.allRooms, roomID)
	// TODO: update lists?
}

func (s *InternalRequestLists) DeleteList(index int) {
	// TODO
}

func (s *InternalRequestLists) Room(roomID string) *RoomConnMetadata {
	r := s.allRooms[roomID]
	return &r
}

func (s *InternalRequestLists) Get(listIndex int) *FilteredSortableRooms {
	return s.lists[listIndex]
}

// Assign a new list at the given index. If Overwrite, any existing list is replaced. If DoNotOverwrite, the existing
// list is returned if one exists, else a new list is created. Returns the list and true if the list was overwritten.
func (s *InternalRequestLists) AssignList(index int, filters *RequestFilters, sort []string, shouldOverwrite OverwriteVal) (*FilteredSortableRooms, bool) {
	internal.Assert("Set index is at most list size", index <= len(s.lists))
	if shouldOverwrite == DoNotOverwrite && index < len(s.lists) {
		return s.lists[index], false
	}
	roomIDs := make([]string, len(s.allRooms))
	i := 0
	for roomID := range s.allRooms {
		roomIDs[i] = roomID
		i++
	}

	roomList := NewFilteredSortableRooms(s, roomIDs, filters)
	if sort != nil {
		err := roomList.Sort(sort)
		if err != nil {
			logger.Err(err).Strs("sort_by", sort).Msg("failed to sort")
		}
	}
	if index == len(s.lists) {
		s.lists = append(s.lists, roomList)
		return roomList, true
	}
	s.lists[index] = roomList
	return roomList, true
}

// Count returns the count of total rooms in this list
func (s *InternalRequestLists) Count(index int) int {
	return int(s.lists[index].Len())
}

func (s *InternalRequestLists) Len() int {
	return len(s.lists)
}
