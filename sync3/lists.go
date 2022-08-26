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

type RoomDelta struct {
	RoomNameChanged bool
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
		delta.RoomNameChanged = !existing.SameRoomName(&r.RoomMetadata)
		if delta.RoomNameChanged {
			// update the canonical name to allow room name sorting to continue to work
			r.CanonicalisedName = strings.ToLower(
				strings.Trim(internal.CalculateRoomName(&r.RoomMetadata, 5), "#!():_@"),
			)
		}
	}
	s.allRooms[r.RoomID] = r
	return delta
}

func (s *InternalRequestLists) AddRoomIfNotExists(room RoomConnMetadata) {
	_, exists := s.allRooms[room.RoomID]
	if !exists {
		s.allRooms[room.RoomID] = room
	}
}

// Remove a room from all lists e.g retired an invite, left a room
func (s *InternalRequestLists) RemoveRoom(roomID string) {
	delete(s.allRooms, roomID)
	// TODO: update lists?
}

// Call the given function for each list. Useful when there is a live update and you don't know
// which list may be updated.
func (s *InternalRequestLists) ForEach(fn func(index int, fsr *FilteredSortableRooms)) {
	for i, l := range s.lists {
		fn(i, l)
	}
}

func (s *InternalRequestLists) DeleteList(index int) {
	// TODO
}

func (s *InternalRequestLists) Room(roomID string) *RoomConnMetadata {
	r := s.allRooms[roomID]
	return &r
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
