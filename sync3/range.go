package sync3

import (
	"fmt"
	"sort"
)

type SliceRanges [][2]int64

func (r SliceRanges) Valid() bool {
	for i, sr := range r {
		// always goes from start to end
		if sr[1] < sr[0] {
			return false
		}
		if sr[0] < 0 {
			return false
		}
		// cannot have overlapping ranges
		for j := i + 1; j < len(r); j++ {
			testRange := r[j]
			// check both ranges with each other
			for _, val := range sr {
				if testRange[0] <= val && val <= testRange[1] {
					return false
				}
			}
			for _, val := range testRange {
				if sr[0] <= val && val <= sr[1] {
					return false
				}
			}
		}
	}
	return true
}

// Inside returns true if i is inside the range
func (r SliceRanges) Inside(i int64) ([2]int64, bool) {
	for _, sr := range r {
		if sr[0] <= i && i <= sr[1] {
			return sr, true
		}
	}
	return [2]int64{}, false
}

// ClosestInDirection returns the index position of a range bound that is closest to `i`, heading either
// towards 0 or towards infinity. If there is no range boundary in that direction, -1 is returned.
// For example:
//
//	[0,20] i=25,towardsZero=true => 20
//	[0,20] i=15,towardsZero=true => 0
//	[0,20] i=15,towardsZero=false => 20
//	[0,20] i=25,towardsZero=false => -1
//	[0,20],[40,60] i=25,towardsZero=true => 20
//	[0,20],[40,60] i=25,towardsZero=false => 40
//	[0,20],[40,60] i=40,towardsZero=true => 40
//	[20,40] i=40,towardsZero=true => 20
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
		// we want the first value < i so loop from low->high and when we see the first >= index, take the previous
		for j := range indexes {
			if indexes[j] >= i {
				if j == 0 {
					if indexes[j] == i {
						return indexes[j]
					}
					return -1
				}
				// if the start of the window IS the index value, return the start of the window range, NOT an earlier range
				if j%2 == 0 && indexes[j] == i {
					return indexes[j]
				} else {
					return indexes[j-1]
				}
			}
		}
		return indexes[len(indexes)-1] // all values are lower than i, choose the highest
	} else {
		// we want the first value > i so loop from high->low and when we see the first <= index, take the previous
		for j := len(indexes) - 1; j >= 0; j-- {
			if indexes[j] <= i {
				if j == len(indexes)-1 {
					if indexes[j] == i {
						return indexes[j]
					}
					return -1
				}
				// if the end of the window IS the index value, return the end of the window range, NOT a later range
				if j%2 == 1 && indexes[j] == i {
					return indexes[j]
				} else {
					return indexes[j+1]
				}
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

func (p pointInfo) same(o *pointInfo) bool {
	return p.x == o.x && p.isOldRange == o.isOldRange && p.isOpen == o.isOpen
}

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
	//
	//  .-----.     .------.  Old ranges
	// -------------------------------------------> number line
	//     `-----`              `-----`  New ranges
	//
	//
	//  .--.        .------.  Old ranges
	// -----==------------------------------------> number line (== same ranges)
	//        `--`              `-----`  New ranges
	// Overlap has old/same/new
	var points []pointInfo // a range = 2x pointInfo on the x-axis
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
	sortPoints(points)

	// sweep from low to high and keep tabs of which point is open
	var openOldPoint *pointInfo
	var openNewPoint *pointInfo
	var lastPoint *pointInfo
	var lastMergedRange *[2]int64
	for i := range points {
		point := points[i]
		// e.g someone does [[0, 20] [0, 20]] in a single range which results in 0,0,20,20
		if lastPoint != nil && point.same(lastPoint) {
			continue
		}

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
			lastMergedRange = &same[len(same)-1]
		} else if openNewPoint != nil { // N->S or N->[]
			mergedRange := createRange(&point, lastPoint, lastMergedRange)
			if mergedRange != nil {
				added = append(added, *mergedRange)
				lastMergedRange = &added[len(added)-1]
			}
		} else if openOldPoint != nil { // O->S or O->[]
			mergedRange := createRange(&point, lastPoint, lastMergedRange)
			if mergedRange != nil {
				removed = append(removed, *mergedRange)
				lastMergedRange = &removed[len(removed)-1]
			}
		}

		// Remember this point
		if point.isOpen {
			// ensure we cannot open more than 1 distinct range on old/new at a time
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

// createRange returns a range by closing off an existing open point. Note the "point" may be
// another open e.g in the case of [0,20] -> [15,25] we want to make the range [0,15] even though 15
// is an open not a close. We don't care if these points are old or new, as that just determines whether
// they were added or removed, it doesn't change the range logic.
func createRange(point, lastPoint *pointInfo, lastMergedRange *[2]int64) *[2]int64 {
	if point.x <= lastPoint.x {
		// don't make 0-length ranges which would be possible in say `[0,20] -> [0,20]`
		return nil
	}

	// we assume we include the last point (i,e it's closing a range after all) but there are edge cases
	// where we dont
	start := lastPoint.x
	if lastMergedRange != nil && lastPoint.x <= lastMergedRange[1] {
		// this can happen for cases like:
		// [0,20] -> [0,20],[20,40] whereby [0,20] is a same range but [20,40] is new, but there is
		// a 1 element overlap. In this scenario, when processing 40, lastPoint.x = 20 and lastMergedRange=[0,20]
		// and lastPoint.isOpen so we won't add 1 to the start index of this range, and won't know that we have
		// to without information on the last merged range.
		start += 1
	} else if !lastPoint.isOpen {
		start += 1
	}

	if point.isOpen {
		// do -1 if 'point' is an open as it will make its own range and we don't want dupes
		return &[2]int64{
			start, point.x - 1,
		}
	} else {
		// 'point' is a close so needs to include itself
		return &[2]int64{
			start, point.x,
		}
	}
}

func sortPoints(points []pointInfo) {
	sort.Slice(points, func(i, j int) bool {
		if points[i].x != points[j].x {
			return points[i].x < points[j].x
		}
		// the x points are the same.
		// consider the delta for:
		// old = [0,20]
		// new = [20, 30]
		// wants:
		// sames: [20,20]
		// news: [21,30]
		// dels: [0,19]
		// Note there are 2x 20s there, so which order should they come in?
		// If we process the closing 20 first, we will not know to subtract 1 to the end range,
		// so we need to make sure all opening values are processed _first_. We also need this
		// to be deterministic so we need to tiebreak on old/new.
		// hence the rules:
		// - open come first, tiebreak old
		// - close come after, tiebreak new
		if points[i].isOpen && !points[j].isOpen {
			return true
		} else if !points[i].isOpen && points[j].isOpen {
			return false
		}
		// both are open or both are closed, tiebreak old first
		if points[i].isOldRange && !points[j].isOldRange {
			return true
		} else if !points[i].isOldRange && points[j].isOldRange {
			return false
		}
		return true // identical
	})
}

type Subslicer interface {
	Len() int64
	Subslice(i, j int64) Subslicer
}
