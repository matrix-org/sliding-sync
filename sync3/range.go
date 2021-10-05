package sync3

import "math"

type SliceRanges [][2]int64

func (r SliceRanges) Valid() bool {
	for _, sr := range r {
		// always goes from start to end
		if sr[1] < sr[0] {
			return false
		}
		if sr[0] < 0 {
			return false
		}
	}
	return true
}

// Inside returns true if i is inside the range
func (r SliceRanges) Inside(i int64) bool {
	for _, sr := range r {
		if sr[0] <= i && i <= sr[1] {
			return true
		}
	}
	return false
}

// UpperClamp returns the start-index e.g [50,99] -> 50 of the first range higher than i.
// If `i` is inside a range, returns -1.
// E.g [50,99] i=30 -> 50, [50,99] i=55 -> -1
func (r SliceRanges) UpperClamp(i int64) (clampIndex int64) {
	clampIndex = math.MaxInt64 - 1
	modified := false
	for _, sr := range r {
		if sr[0] > i && sr[0] < int64(clampIndex) {
			clampIndex = sr[0]
			modified = true
		}
	}
	if !modified {
		clampIndex = -1
	}
	return
}

// LowerClamp returns the end-index e.g [0,99] -> 99 of the first range lower than i.
// This is critical to determine which index to delete when rooms move outside of the tracked range.
// If `i` is inside a range, returns the clamp for the lower range. Returns -1 if a clamp cannot be found
// e.g [0,99] i=50 -> -1 whereas [0,99][150,199] i=160 -> 99
func (r SliceRanges) LowerClamp(i int64) (clampIndex int64) {
	clampIndex = -1
	for _, sr := range r {
		if sr[1] < i && sr[1] > int64(clampIndex) {
			clampIndex = sr[1]
		}
	}
	return
}

// Delta returns the ranges which are unchanged, added and removed
func (r SliceRanges) Delta(next SliceRanges) (added SliceRanges, removed SliceRanges, same SliceRanges) {
	olds := make(map[[2]int64]bool)
	for _, oldStartEnd := range r {
		olds[oldStartEnd] = true
	}
	news := make(map[[2]int64]bool)
	for _, newStartEnd := range next {
		news[newStartEnd] = true
	}

	for oldStartEnd := range olds {
		if news[oldStartEnd] {
			same = append(same, oldStartEnd)
		} else {
			removed = append(removed, oldStartEnd)
		}
	}
	for newStartEnd := range news {
		if olds[newStartEnd] {
			continue
		}
		added = append(added, newStartEnd)
	}
	return
}

// Slice into this range, returning subslices of slice
func (r SliceRanges) SliceInto(slice Subslicer) []Subslicer {
	var result []Subslicer
	// TODO: ensure we don't have overlapping ranges
	for _, sr := range r {
		// apply range caps
		// the range are always index positions hence -1
		sliceLen := slice.Len()
		if sr[0] >= sliceLen {
			sr[0] = sliceLen - 1
		}
		if sr[1] >= sliceLen {
			sr[1] = sliceLen - 1
		}
		subslice := slice.Subslice(sr[0], sr[1]+1)
		result = append(result, subslice)
	}
	return result
}

type Subslicer interface {
	Len() int64
	Subslice(i, j int64) Subslicer
}
