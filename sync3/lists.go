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
	allRooms []RoomConnMetadata // TODO: should this be a map?
	lists    []*FilteredSortableRooms
}

func (s *InternalRequestLists) AddRooms(rooms []RoomConnMetadata) {
	s.allRooms = append(s.allRooms, rooms...)
}

func (s *InternalRequestLists) AddRoomIfNotExists(room RoomConnMetadata) {
	exists := false
	for _, r := range s.allRooms {
		if r.RoomID == room.RoomID {
			exists = true
			break
		}
	}
	if !exists {
		s.allRooms = append(s.allRooms, room)
	}
}

// Remove a room from all lists e.g retired an invite, left a room
func (s *InternalRequestLists) RemoveRoom(roomID string) {
	for i, r := range s.allRooms {
		if r.RoomID == roomID {
			// delete the room
			s.allRooms[i] = s.allRooms[len(s.allRooms)-1]
			s.allRooms = s.allRooms[:len(s.allRooms)-1]
		}
	}
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
