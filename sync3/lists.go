package sync3

import "github.com/matrix-org/sync-v3/internal"

type OverwriteVal bool

var (
	DoNotOverwrite OverwriteVal = false
	Overwrite      OverwriteVal = true
)

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

func (s *InternalRequestLists) AddRooms(rooms []RoomConnMetadata) {
	for _, r := range rooms {
		s.allRooms[r.RoomID] = r
	}

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

// Update the room metadata entry for this room. If the room name is the same with the new metadata, returns true.
func (s *InternalRequestLists) UpdateRoom(metadata *internal.RoomMetadata) (sameRoomName bool) {
	// find the room
	existing, exists := s.allRooms[metadata.RoomID]
	if !exists {
		return false
	}
	sameRoomName = existing.SameRoomName(metadata)
	existing.RoomMetadata = *metadata
	s.allRooms[metadata.RoomID] = existing
	return
}

func (s *InternalRequestLists) DeleteList(index int) {
	// TODO
}

// Assign a new list at the given index. If Overwrite, any existing list is replaced. If DoNotOverwrite, the existing
// list is returned if one exists, else a new list is created. Returns the list and true if the list was overwritten.
func (s *InternalRequestLists) AssignList(index int, filters *RequestFilters, sort []string, shouldOverwrite OverwriteVal) (*FilteredSortableRooms, bool) {
	internal.Assert("Set index is at most list size", index <= len(s.lists))
	if shouldOverwrite == DoNotOverwrite && index < len(s.lists) {
		return s.lists[index], false
	}
	roomList := NewFilteredSortableRooms(s.allRooms, filters)
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
