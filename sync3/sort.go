package sync3

import (
	"fmt"
	"sort"

	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sync3/caches"
)

// SortableRooms represents a list of rooms which can be sorted and updated. Maintains mappings of
// room IDs to current index positions after sorting.
type SortableRooms struct {
	rooms         []RoomConnMetadata
	roomIDToIndex map[string]int // room_id -> index in rooms
}

func NewSortableRooms(rooms []RoomConnMetadata) *SortableRooms {
	return &SortableRooms{
		rooms:         rooms,
		roomIDToIndex: make(map[string]int),
	}
}

func (s *SortableRooms) UpdateGlobalRoomMetadata(roomMeta *internal.RoomMetadata) int {
	pos, ok := s.roomIDToIndex[roomMeta.RoomID]
	if !ok {
		return -1
	}
	meta := s.rooms[pos]
	meta.RoomMetadata = *roomMeta
	s.rooms[pos] = meta
	return -1
}

func (s *SortableRooms) UpdateUserRoomMetadata(roomID string, userEvent *caches.UserRoomData) int {
	index, ok := s.roomIDToIndex[roomID]
	if !ok {
		return -1
	}
	meta := s.rooms[index]
	meta.UserRoomData = *userEvent
	s.rooms[index] = meta
	return -1
}

func (s *SortableRooms) IndexOf(roomID string) (int, bool) {
	index, ok := s.roomIDToIndex[roomID]
	return index, ok
}

func (s *SortableRooms) RoomIDs() []string {
	roomIDs := make([]string, len(s.rooms))
	for i := range s.rooms {
		roomIDs[i] = s.rooms[i].RoomID
	}
	return roomIDs
}

// Add a room to the list. Returns true if the room was added.
func (s *SortableRooms) Add(r RoomConnMetadata) bool {
	_, exists := s.roomIDToIndex[r.RoomID]
	if exists {
		return false
	}
	s.rooms = append(s.rooms, r)
	s.roomIDToIndex[r.RoomID] = len(s.rooms) - 1
	return true
}

func (s *SortableRooms) Get(index int) RoomConnMetadata {
	internal.Assert("index is within len(rooms)", index < len(s.rooms))
	return s.rooms[index]
}

func (s *SortableRooms) Remove(roomID string) int {
	index, ok := s.roomIDToIndex[roomID]
	if !ok {
		return -1
	}
	delete(s.roomIDToIndex, roomID)
	// splice out index
	s.rooms = append(s.rooms[:index], s.rooms[index+1:]...)
	return index
}

func (s *SortableRooms) Len() int64 {
	return int64(len(s.rooms))
}
func (s *SortableRooms) Subslice(i, j int64) Subslicer {
	internal.Assert("i < j and are within len(rooms)", i < j && i < int64(len(s.rooms)) && j <= int64(len(s.rooms)))
	return &SortableRooms{
		rooms:         s.rooms[i:j],
		roomIDToIndex: s.roomIDToIndex,
	}
}

func (s *SortableRooms) Sort(sortBy []string) error {
	internal.Assert("sortBy is not empty", len(sortBy) != 0)
	comparators := []func(i, j int) int{}
	for _, sort := range sortBy {
		switch sort {
		case SortByHighlightCount:
			comparators = append(comparators, s.comparatorSortByHighlightCount)
		case SortByNotificationCount:
			comparators = append(comparators, s.comparatorSortByNotificationCount)
		case SortByName:
			comparators = append(comparators, s.comparatorSortByName)
		case SortByRecency:
			comparators = append(comparators, s.comparatorSortByRecency)
		default:
			return fmt.Errorf("unknown sort order: %s", sort)
		}
	}
	sort.SliceStable(s.rooms, func(i, j int) bool {
		for _, fn := range comparators {
			val := fn(i, j)
			if val == 1 {
				return true
			} else if val == -1 {
				return false
			}
			// continue to next comparator as these are equal
		}
		// the two items are identical
		return false
	})

	for i := range s.rooms {
		s.roomIDToIndex[s.rooms[i].RoomID] = i
	}

	return nil
}

// Comparator functions: -1 = false, +1 = true, 0 = match

func (s *SortableRooms) comparatorSortByName(i, j int) int {
	if s.rooms[i].CanonicalisedName == s.rooms[j].CanonicalisedName {
		return 0
	}
	if s.rooms[i].CanonicalisedName < s.rooms[j].CanonicalisedName {
		return 1
	}
	return -1
}

func (s *SortableRooms) comparatorSortByRecency(i, j int) int {
	if s.rooms[i].LastMessageTimestamp == s.rooms[j].LastMessageTimestamp {
		return 0
	}
	if s.rooms[i].LastMessageTimestamp > s.rooms[j].LastMessageTimestamp {
		return 1
	}
	return -1
}

func (s *SortableRooms) comparatorSortByHighlightCount(i, j int) int {
	if s.rooms[i].HighlightCount == s.rooms[j].HighlightCount {
		return 0
	}
	if s.rooms[i].HighlightCount > s.rooms[j].HighlightCount {
		return 1
	}
	return -1
}

func (s *SortableRooms) comparatorSortByNotificationCount(i, j int) int {
	if s.rooms[i].NotificationCount == s.rooms[j].NotificationCount {
		return 0
	}
	if s.rooms[i].NotificationCount > s.rooms[j].NotificationCount {
		return 1
	}
	return -1
}

// FilteredSortableRooms is SortableRooms but where rooms are filtered before being added to the list.
// Updates to room metadata may result in rooms being added/removed.
type FilteredSortableRooms struct {
	*SortableRooms
	filter *RequestFilters
}

func NewFilteredSortableRooms(rooms []RoomConnMetadata, filter *RequestFilters) *FilteredSortableRooms {
	var filteredRooms []RoomConnMetadata
	if filter == nil {
		filter = &RequestFilters{}
	}
	for _, r := range rooms {
		if filter.Include(&r) {
			filteredRooms = append(filteredRooms, r)
		}
	}
	return &FilteredSortableRooms{
		SortableRooms: NewSortableRooms(filteredRooms),
		filter:        filter,
	}
}

func (f *FilteredSortableRooms) Add(r RoomConnMetadata) bool {
	if !f.filter.Include(&r) {
		return false
	}
	return f.SortableRooms.Add(r)
}

func (f *FilteredSortableRooms) UpdateGlobalRoomMetadata(r *internal.RoomMetadata) int {
	index, ok := f.SortableRooms.IndexOf(r.RoomID)
	if !ok {
		return -1
	}
	// Get must return a copy as we are modifying it eagerly to pass to the filter
	room := f.SortableRooms.Get(index)
	room.RoomMetadata = *r
	if !f.filter.Include(&room) {
		return f.Remove(r.RoomID)
	}
	return f.SortableRooms.UpdateGlobalRoomMetadata(r)
}

func (f *FilteredSortableRooms) UpdateUserRoomMetadata(roomID string, userEvent *caches.UserRoomData) int {
	index, ok := f.SortableRooms.IndexOf(roomID)
	if !ok {
		return -1
	}
	// Get must return a copy as we are modifying it eagerly to pass to the filter
	room := f.SortableRooms.Get(index)
	room.UserRoomData = *userEvent
	if !f.filter.Include(&room) {
		return f.Remove(roomID)
	}
	return f.SortableRooms.UpdateUserRoomMetadata(roomID, userEvent)
}
