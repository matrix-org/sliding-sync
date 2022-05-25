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

// Count returns the count of total rooms in this list
func (s *SortableRoomLists) Count(index int) int {
	return int(s.lists[index].Len())
}

func (s *SortableRoomLists) Len() int {
	return len(s.lists)
}
