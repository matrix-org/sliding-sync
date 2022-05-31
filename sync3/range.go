package sync3

import (
	"fmt"
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

// ClosestInDirection returns the index position of a range bound that is closest to `i`, heading either
// towards 0 or towards infinity. If there is no range boundary in that direction, -1 is returned.
// For example:
//   [0,20] i=25,towardsZero=true => 20
//   [0,20] i=15,towardsZero=true => 0
//   [0,20] i=15,towardsZero=false => 20
//   [0,20] i=25,towardsZero=false => -1
//   [0,20],[40,60] i=25,towardsZero=true => 20
//   [0,20],[40,60] i=25,towardsZero=false => 40
func (r SliceRanges) ClosestInDirection(i int64, towardsZero bool) (closestIndex int64) {
	// sort all range boundaries in ascending order
	indexes := make([]int64, 0, len(r)*2)
	for _, sr := range r {
		indexes = append(indexes, sr[0], sr[1])
	}
	sort.Slice(indexes, func(x, y int) bool {
		return indexes[x] < indexes[y]
	})
	closestIndex = -1

	if towardsZero {
		// we want the first value < i so loop from low->high and when we see the first > index, take the previous
		for j := range indexes {
			if indexes[j] > i {
				if j == 0 {
					return -1
				}
				return indexes[j-1]
			}
		}
		return indexes[len(indexes)-1] // all values are lower than i, choose the highest
	} else {
		// we want the first value > i so loop from high->low and when we see the first < index, take the previous
		for j := len(indexes) - 1; j >= 0; j-- {
			if indexes[j] < i {
				if j == len(indexes)-1 {
					return -1
				}
				return indexes[j+1]
			}
		}
		return indexes[0] // all values are higher than i, choose the lowest
	}
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
