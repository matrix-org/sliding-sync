package sync3

import (
	"fmt"
	"sort"
)

type SortableRooms []RoomConnMetadata

func (s SortableRooms) Len() int64 {
	return int64(len(s))
}
func (s SortableRooms) Subslice(i, j int64) Subslicer {
	return s[i:j]
}

func (s SortableRooms) Sort(sortBy []string) error {
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
	sort.SliceStable(s, func(i, j int) bool {
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

	return nil
}

// Comparator functions: -1 = false, +1 = true, 0 = match

func (s SortableRooms) comparatorSortByName(i, j int) int {
	if s[i].CanonicalisedName == s[j].CanonicalisedName {
		return 0
	}
	if s[i].CanonicalisedName < s[j].CanonicalisedName {
		return 1
	}
	return -1
}

func (s SortableRooms) comparatorSortByRecency(i, j int) int {
	if s[i].LastMessageTimestamp == s[j].LastMessageTimestamp {
		return 0
	}
	if s[i].LastMessageTimestamp > s[j].LastMessageTimestamp {
		return 1
	}
	return -1
}

func (s SortableRooms) comparatorSortByHighlightCount(i, j int) int {
	if s[i].HighlightCount == s[j].HighlightCount {
		return 0
	}
	if s[i].HighlightCount > s[j].HighlightCount {
		return 1
	}
	return -1
}

func (s SortableRooms) comparatorSortByNotificationCount(i, j int) int {
	if s[i].NotificationCount == s[j].NotificationCount {
		return 0
	}
	if s[i].NotificationCount > s[j].NotificationCount {
		return 1
	}
	return -1
}
