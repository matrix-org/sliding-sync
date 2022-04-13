package sync3

import (
	"fmt"
	"math"
	"sort"
)

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

type pointInfo struct {
	x          int64
	isOldRange bool
	isOpen     bool
}

// TODO: A,B,C track A,B then B,C incorrectly keeps B?

// Delta returns the ranges which are unchanged, added and removed.
// Intelligently handles overlaps.
func (r SliceRanges) Delta(next SliceRanges) (added SliceRanges, removed SliceRanges, same SliceRanges) {
	added = [][2]int64{}
	removed = [][2]int64{}
	same = [][2]int64{}
	// short circuit for same ranges
	if len(r) == len(next) {
		isSame := true
		for i := range next {
			if r[i] != next[i] {
				isSame = false
				break
			}
		}
		if isSame {
			same = next
			return
		}
	}

	// sort all points from min to max then do a sweep line algorithm over it with open/closed lists
	// to track overlaps, runtime O(nlogn) due to sorting points
	//              Old ranges
	//  .-----.     .------.
	// -------------------------------------------> number line
	//     `-----`              `-----`
	//              New ranges
	//
	//  .--.        .------.
	// -----==------------------------------------> number line
	//        `--`              `-----`
	// Overlap has old/same/new
	var points []pointInfo // a range = 2 points on the x-axis
	for _, oldRange := range r {
		points = append(points, pointInfo{
			x:          oldRange[0],
			isOldRange: true,
			isOpen:     true,
		})
		points = append(points, pointInfo{
			x:          oldRange[1],
			isOldRange: true,
		})
	}
	for _, newRange := range next {
		points = append(points, pointInfo{
			x:          newRange[0],
			isOldRange: false,
			isOpen:     true,
		})
		points = append(points, pointInfo{
			x:          newRange[1],
			isOldRange: false,
		})
	}
	sort.Slice(points, func(i, j int) bool {
		return points[i].x < points[j].x
	})

	// sweep from low to high and keep tabs of which point is open
	var openOldPoint *pointInfo
	var openNewPoint *pointInfo
	var lastPoint *pointInfo
	for i := range points {
		point := points[i]
		// We are effectively tracking a finite state machine that looks like:
		//
		//   .------> O <--*--.
		//  []<---*---|       |--> S
		//   `------> N <--*--`
		//
		// [] = no open points, O = old point open, N = new point open, S = old and new points open
		//
		// State transitions causes ranges to be opened or closed. When ranges start, we just remember
		// the point ([] -> O and [] -> N). When ranges are closed, we create a range. Arrows with
		// a * in the path indicates a range is created when this state transition occurs.

		if openNewPoint != nil && openOldPoint != nil { // S->O or S->N
			// this point is a close so we overlap with the previous point,
			// which could be an open for O or N, we don't care.
			same = append(same, [2]int64{
				lastPoint.x, point.x,
			})
		} else if openNewPoint != nil { // N->S or N->[]
			if point.isOpen { // N->S
				// only add the range [N, S-1] if this point is NOT the same as N otherwise
				// we will add an incorrect range. In the case where the O and N range are identical
				// we will already add the range when the outermost range closes (transitioning ->[])
				// This code is duplicated for the old point further down.
				lastPointSame := lastPoint.x == point.x
				if point.x > openNewPoint.x && !lastPointSame {
					added = append(added, [2]int64{
						openNewPoint.x, point.x - 1,
					})
				}
			} else { // N->[]
				// do not create 2 ranges for O=[1,5] N=[1,5]. Skip the innermost close, which is defined
				// as the last point closing with the same x-value as this close point.
				lastPointSameClose := !lastPoint.isOpen && lastPoint.x == point.x
				if !lastPointSameClose {
					pos := lastPoint.x
					if !lastPoint.isOpen {
						pos += 1 // the last point was a close for an overlap so we need to shift index by one
					}
					added = append(added, [2]int64{
						pos, point.x,
					})
				}
			}
		} else if openOldPoint != nil { // O->S or O->[]
			if point.isOpen { // O->S
				// See above comments.
				lastPointSame := lastPoint.x == point.x
				if point.x > openOldPoint.x && !lastPointSame {
					removed = append(removed, [2]int64{
						openOldPoint.x, point.x - 1,
					})
				}
			} else { // O->[]
				// See above comments.
				lastPointSameClose := !lastPoint.isOpen && lastPoint.x == point.x
				if !lastPointSameClose {
					pos := lastPoint.x
					if !lastPoint.isOpen {
						pos += 1 // the last point was a close for an overlap so we need to shift index by one
					}
					removed = append(removed, [2]int64{
						pos, point.x,
					})
				}
			}
		}

		// Remember this point
		if point.isOpen {
			// ensure we cannot open more than 1 range on old/new at a time
			if (point.isOldRange && openOldPoint != nil) || (!point.isOldRange && openNewPoint != nil) {
				panic(fmt.Sprintf("point already open! old=%v new=%v", r, next))
			}
			if point.isOldRange {
				openOldPoint = &point
			} else {
				openNewPoint = &point
			}
		} else {
			// ensure we cannot close more than 1 range on old/new at a time
			if (point.isOldRange && openOldPoint == nil) || (!point.isOldRange && openNewPoint == nil) {
				panic(fmt.Sprintf("point already closed! old=%v new=%v", r, next))
			}
			if point.isOldRange {
				openOldPoint = nil
			} else {
				openNewPoint = nil
			}
		}
		lastPoint = &point
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
		if sliceLen == 0 {
			// empty slices subslice into themselves
			continue
		}
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
