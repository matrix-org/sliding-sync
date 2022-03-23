package sync3

import "github.com/matrix-org/sync-v3/internal"

// SortableRoomLists is a list of FilteredSortableRooms.
type SortableRoomLists struct {
	lists []*FilteredSortableRooms
}

func (s *SortableRoomLists) ListExists(index int) bool {
	return index < len(s.lists) && index >= 0
}

func (s *SortableRoomLists) List(index int) *FilteredSortableRooms {
	internal.Assert("index within range", index < len(s.lists))
	return s.lists[index]
}

func (s *SortableRoomLists) ForEach(fn func(index int, fsr *FilteredSortableRooms)) {
	for i, l := range s.lists {
		fn(i, l)
	}
}

func (s *SortableRoomLists) Set(index int, val *FilteredSortableRooms) {
	internal.Assert("Set index is at most list size", index <= len(s.lists))
	if index == len(s.lists) {
		s.lists = append(s.lists, val)
		return
	}
	s.lists[index] = val
}

// Counts returns the counts of all lists
func (s *SortableRoomLists) Counts() []int {
	counts := make([]int, len(s.lists))
	for i := range s.lists {
		counts[i] = int(s.lists[i].Len())
	}
	return counts
}
