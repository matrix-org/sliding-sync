package sync3

import (
	"fmt"
	"sort"

	"github.com/matrix-org/sync-v3/internal"
)

type RoomFinder interface {
	Room(roomID string) *RoomConnMetadata
}

// SortableRooms represents a list of rooms which can be sorted and updated. Maintains mappings of
// room IDs to current index positions after sorting.
type SortableRooms struct {
	finder        RoomFinder
	roomIDs       []string
	roomIDToIndex map[string]int // room_id -> index in rooms
}

func NewSortableRooms(finder RoomFinder, rooms []string) *SortableRooms {
	return &SortableRooms{
		roomIDs:       rooms,
		finder:        finder,
		roomIDToIndex: make(map[string]int),
	}
}

func (s *SortableRooms) IndexOf(roomID string) (int, bool) {
	index, ok := s.roomIDToIndex[roomID]
	return index, ok
}

func (s *SortableRooms) RoomIDs() []string {
	roomIDs := make([]string, len(s.roomIDs))
	for i := range s.roomIDs {
		roomIDs[i] = s.roomIDs[i]
	}
	return roomIDs
}

// Add a room to the list. Returns true if the room was added.
func (s *SortableRooms) Add(roomID string) bool {
	_, exists := s.roomIDToIndex[roomID]
	if exists {
		return false
	}
	s.roomIDs = append(s.roomIDs, roomID)
	s.roomIDToIndex[roomID] = len(s.roomIDs) - 1
	return true
}

func (s *SortableRooms) Get(index int) string {
	internal.Assert("index is within len(rooms)", index < len(s.roomIDs))
	return s.roomIDs[index]
}

func (s *SortableRooms) Remove(roomID string) int {
	index, ok := s.roomIDToIndex[roomID]
	if !ok {
		return -1
	}
	delete(s.roomIDToIndex, roomID)
	// splice out index
	s.roomIDs = append(s.roomIDs[:index], s.roomIDs[index+1:]...)
	// re-update the map
	for i := index; i < len(s.roomIDs); i++ {
		s.roomIDToIndex[s.roomIDs[i]] = i
	}
	return index
}

func (s *SortableRooms) Len() int64 {
	return int64(len(s.roomIDs))
}
func (s *SortableRooms) Subslice(i, j int64) Subslicer {
	internal.Assert("i < j and are within len(rooms)", i < j && i < int64(len(s.roomIDs)) && j <= int64(len(s.roomIDs)))
	return &SortableRooms{
		roomIDs:       s.roomIDs[i:j],
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
	sort.SliceStable(s.roomIDs, func(i, j int) bool {
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
	for i := range s.roomIDs {
		s.roomIDToIndex[s.roomIDs[i]] = i
	}

	return nil
}

// Comparator functions: -1 = false, +1 = true, 0 = match

func (s *SortableRooms) resolveRooms(i, j int) (ri, rj *RoomConnMetadata) {
	ri = s.finder.Room(s.roomIDs[i])
	rj = s.finder.Room(s.roomIDs[j])
	return
}

func (s *SortableRooms) comparatorSortByName(i, j int) int {
	ri, rj := s.resolveRooms(i, j)
	if ri.CanonicalisedName == rj.CanonicalisedName {
		return 0
	}
	if ri.CanonicalisedName < rj.CanonicalisedName {
		return 1
	}
	return -1
}

func (s *SortableRooms) comparatorSortByRecency(i, j int) int {
	ri, rj := s.resolveRooms(i, j)
	if ri.LastMessageTimestamp == rj.LastMessageTimestamp {
		return 0
	}
	if ri.LastMessageTimestamp > rj.LastMessageTimestamp {
		return 1
	}
	return -1
}

func (s *SortableRooms) comparatorSortByHighlightCount(i, j int) int {
	ri, rj := s.resolveRooms(i, j)
	if ri.HighlightCount == rj.HighlightCount {
		return 0
	}
	if ri.HighlightCount > rj.HighlightCount {
		return 1
	}
	return -1
}

func (s *SortableRooms) comparatorSortByNotificationCount(i, j int) int {
	ri, rj := s.resolveRooms(i, j)
	if ri.NotificationCount == rj.NotificationCount {
		return 0
	}
	if ri.NotificationCount > rj.NotificationCount {
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

func NewFilteredSortableRooms(finder RoomFinder, roomIDs []string, filter *RequestFilters) *FilteredSortableRooms {
	var filteredRooms []string
	if filter == nil {
		filter = &RequestFilters{}
	}
	for _, roomID := range roomIDs {
		r := finder.Room(roomID)
		if filter.Include(r) {
			filteredRooms = append(filteredRooms, roomID)
		}
	}
	return &FilteredSortableRooms{
		SortableRooms: NewSortableRooms(finder, filteredRooms),
		filter:        filter,
	}
}

func (f *FilteredSortableRooms) Add(roomID string) bool {
	r := f.finder.Room(roomID)
	if !f.filter.Include(r) {
		return false
	}
	return f.SortableRooms.Add(roomID)
}

func (f *FilteredSortableRooms) UpdateGlobalRoomMetadata(roomID string) int {
	r := f.finder.Room(roomID)
	internal.Assert("missing room metadata", r != nil)
	_, ok := f.SortableRooms.IndexOf(r.RoomID)
	if !ok {
		return -1
	}
	if !f.filter.Include(r) {
		return f.Remove(r.RoomID)
	}
	return -1
}

func (f *FilteredSortableRooms) UpdateUserRoomMetadata(roomID string) int {
	r := f.finder.Room(roomID)
	_, ok := f.SortableRooms.IndexOf(roomID)
	if !ok {
		return -1
	}
	if !f.filter.Include(r) {
		return f.Remove(roomID)
	}
	return -1
}
