package sync3

import (
	"fmt"
	"sort"

	"github.com/matrix-org/sliding-sync/internal"
)

type RoomFinder interface {
	ReadOnlyRoom(roomID string) *RoomConnMetadata
}

// SortableRooms represents a list of rooms which can be sorted and updated. Maintains mappings of
// room IDs to current index positions after sorting.
type SortableRooms struct {
	finder        RoomFinder
	listKey       string
	roomIDs       []string
	roomIDToIndex map[string]int // room_id -> index in rooms
}

func NewSortableRooms(finder RoomFinder, listKey string, rooms []string) *SortableRooms {
	return &SortableRooms{
		roomIDs:       rooms,
		finder:        finder,
		listKey:       listKey,
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
	// TODO: find a way to plumb a context into this assert
	internal.Assert(fmt.Sprintf("index is within len(rooms) %v < %v", index, len(s.roomIDs)), index < len(s.roomIDs))
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
	// TODO: find a way to plumb a context.Context through to this assert
	internal.Assert("i < j and are within len(rooms)", i < j && i < int64(len(s.roomIDs)) && j <= int64(len(s.roomIDs)))
	return &SortableRooms{
		roomIDs:       s.roomIDs[i:j],
		roomIDToIndex: s.roomIDToIndex,
	}
}

func (s *SortableRooms) Sort(sortBy []string) error {
	// TODO: find a way to plumb a context into this assert
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
		case SortByNotificationLevel:
			comparators = append(comparators, s.comparatorSortByNotificationLevel)
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
	ri = s.finder.ReadOnlyRoom(s.roomIDs[i])
	rj = s.finder.ReadOnlyRoom(s.roomIDs[j])
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
	tsRi := ri.GetLastInterestedEventTimestamp(s.listKey)
	tsRj := rj.GetLastInterestedEventTimestamp(s.listKey)
	if tsRi == tsRj {
		return 0
	}
	if tsRi > tsRj {
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

func (s *SortableRooms) comparatorSortByNotificationLevel(i, j int) int {
	ri, rj := s.resolveRooms(i, j)
	// highlight rooms come first
	if ri.HighlightCount > 0 && rj.HighlightCount > 0 {
		return 0
	}
	if ri.HighlightCount > 0 {
		return 1
	} else if rj.HighlightCount > 0 {
		return -1
	}

	// then notification count
	if ri.NotificationCount > 0 && rj.NotificationCount > 0 {
		// when we are comparing rooms with notif counts, sort encrypted rooms above unencrypted rooms
		// as the client needs to calculate highlight counts (so it's possible that notif counts are
		// actually highlight counts!) - this is the "Lite" description in MSC3575
		if ri.Encrypted && !rj.Encrypted {
			return 1
		} else if rj.Encrypted && !ri.Encrypted {
			return -1
		}
		return 0
	}
	if ri.NotificationCount > 0 {
		return 1
	} else if rj.NotificationCount > 0 {
		return -1
	}
	// no highlight or notifs get grouped together
	return 0
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

func NewFilteredSortableRooms(finder RoomFinder, listKey string, roomIDs []string, filter *RequestFilters) *FilteredSortableRooms {
	var filteredRooms []string
	if filter == nil {
		filter = &RequestFilters{}
	}
	for _, roomID := range roomIDs {
		r := finder.ReadOnlyRoom(roomID)
		if filter.Include(r, finder) {
			filteredRooms = append(filteredRooms, roomID)
		}
	}
	return &FilteredSortableRooms{
		SortableRooms: NewSortableRooms(finder, listKey, filteredRooms),
		filter:        filter,
	}
}

func (f *FilteredSortableRooms) Add(roomID string) bool {
	r := f.finder.ReadOnlyRoom(roomID)
	if !f.filter.Include(r, f.finder) {
		return false
	}
	return f.SortableRooms.Add(roomID)
}
