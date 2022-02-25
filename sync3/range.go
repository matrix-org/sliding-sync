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

// Delta returns the ranges which are unchanged, added and removed.
// Intelligently handles overlaps.
func (r SliceRanges) Delta(next SliceRanges) (added SliceRanges, removed SliceRanges, same SliceRanges) {
	added = [][2]int64{}
	removed = [][2]int64{}
	same = [][2]int64{}
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
		// Possible permutations:
		// 1- have open old point, now being closed => removed (iff last point is not a close for the same x value)
		// 2- have open old point, now with open new point => removed-1 (iff points differ, else [0,9] and [0,9] would insert into diff)
		// 3- have open new point, now being closed => added
		// 4- have open new point, now with open old point => added-1 (iff points differ)
		// 5- have both open points, old|new being closed => same

		if openNewPoint != nil && openOldPoint != nil { // 5
			// this point is a close so we overlap with the previous point
			same = append(same, [2]int64{
				lastPoint.x, point.x,
			})
		} else if openNewPoint != nil { // 3,4
			if point.isOpen { // 4
				lastPointSame := lastPoint.x == point.x
				if point.x > openNewPoint.x && !lastPointSame {
					added = append(added, [2]int64{
						openNewPoint.x, point.x - 1,
					})
				}
			} else { // 3
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
		} else if openOldPoint != nil { // 1,2
			if point.isOpen { // 2
				lastPointSame := lastPoint.x == point.x
				if point.x > openOldPoint.x && !lastPointSame {
					removed = append(removed, [2]int64{
						openOldPoint.x, point.x - 1,
					})
				}
			} else { // 1
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
