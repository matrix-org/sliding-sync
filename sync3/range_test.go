package sync3

import (
	"reflect"
	"testing"
)

type stringSlice []string

func (s stringSlice) Len() int64 {
	return int64(len(s))
}
func (s stringSlice) Subslice(i, j int64) Subslicer {
	return s[i:j]
}

func TestRangeValid(t *testing.T) {
	testCases := []struct {
		input SliceRanges
		valid bool
	}{
		{
			input: SliceRanges([][2]int64{
				{0, 9},
			}),
			valid: true,
		},
		{
			input: SliceRanges([][2]int64{
				{9, 0},
			}),
			valid: false,
		},
		{
			input: SliceRanges([][2]int64{
				{9, 9},
			}),
			valid: true,
		},
		{
			input: SliceRanges([][2]int64{
				{-3, 3},
			}),
			valid: false,
		},
		{
			input: SliceRanges([][2]int64{
				{0, 20}, {20, 40}, // 20 overlaps
			}),
			valid: false,
		},
		{
			input: SliceRanges([][2]int64{
				{0, 20}, {40, 60}, {30, 35},
			}),
			valid: true,
		},
		{
			input: SliceRanges([][2]int64{
				{0, 20}, {40, 60}, {10, 15},
			}),
			valid: false,
		},
		{
			input: SliceRanges([][2]int64{
				{10, 15}, {40, 60}, {0, 20},
			}),
			valid: false,
		},
	}
	for _, tc := range testCases {
		gotValid := tc.input.Valid()
		if gotValid != tc.valid {
			t.Errorf("test case %+v returned valid=%v", tc, gotValid)
		}
	}
}

func TestRange(t *testing.T) {
	alphabet := []string{
		"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z",
	}
	testCases := []struct {
		input SliceRanges
		want  [][]string
	}{
		{
			input: SliceRanges([][2]int64{
				{0, 9},
			}),
			want: [][]string{
				alphabet[0:10],
			},
		},
		{
			input: SliceRanges([][2]int64{
				{0, 99},
			}),
			want: [][]string{
				alphabet,
			},
		},
		{
			input: SliceRanges([][2]int64{
				{0, 0}, {1, 1},
			}),
			want: [][]string{
				alphabet[0:1], alphabet[1:2],
			},
		},
		{
			input: SliceRanges([][2]int64{
				{30, 99},
			}),
			want: [][]string{},
		},
	}

	for _, tc := range testCases {
		result := tc.input.SliceInto(stringSlice(alphabet))
		if len(result) != len(tc.want) {
			t.Errorf("%+v subslice mismatch: got %v want %v \ngot:  %+v\nwant: %+v\n", tc, len(result), len(tc.want), result, tc.want)
			continue
		}
		for i := range result {
			got := result[i].(stringSlice)
			want := tc.want[i]
			if !reflect.DeepEqual([]string(got), want) {
				t.Errorf("%+v wrong subslice returned, got %v want %v", tc, got, want)
			}
		}

	}
}

func TestRangeInside(t *testing.T) {
	testCases := []struct {
		testRange SliceRanges
		i         int64
		inside    bool
	}{
		{
			testRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			i:      6,
			inside: true,
		},
		{
			testRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			i:      0,
			inside: true,
		},
		{
			testRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			i:      9,
			inside: true,
		},
		{
			testRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			i:      -1,
			inside: false,
		},
		{
			testRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			i:      10,
			inside: false,
		},
		{
			testRange: SliceRanges([][2]int64{
				{0, 9}, {10, 19},
			}),
			i:      10,
			inside: true,
		},
		{
			testRange: SliceRanges([][2]int64{
				{0, 9}, {20, 29},
			}),
			i:      10,
			inside: false,
		},
	}
	for _, tc := range testCases {
		_, gotInside := tc.testRange.Inside(tc.i)
		if gotInside != tc.inside {
			t.Errorf("%+v got Inside:%v want %v", tc, gotInside, tc.inside)
		}
	}
}

func TestRangeClosestInDirection(t *testing.T) {
	testCases := []struct {
		ranges           SliceRanges
		i                int64
		towardsZero      bool
		wantClosestIndex int64
	}{
		{
			ranges:           [][2]int64{{0, 20}},
			i:                15,
			towardsZero:      true,
			wantClosestIndex: 0,
		},
		{
			ranges:           [][2]int64{{0, 20}},
			i:                0,
			towardsZero:      false,
			wantClosestIndex: 20,
		},
		{
			ranges:           [][2]int64{{0, 20}},
			i:                20,
			towardsZero:      true,
			wantClosestIndex: 0,
		},
		{
			ranges:           [][2]int64{{0, 20}},
			i:                0,
			towardsZero:      true,
			wantClosestIndex: 0,
		},
		{
			ranges:           [][2]int64{{0, 20}},
			i:                20,
			towardsZero:      false,
			wantClosestIndex: 20,
		},
		{
			ranges:           [][2]int64{{0, 20}},
			i:                15,
			towardsZero:      false,
			wantClosestIndex: 20,
		},
		{
			ranges:           [][2]int64{{10, 20}},
			i:                5,
			towardsZero:      true,
			wantClosestIndex: -1,
		},
		{
			ranges:           [][2]int64{{10, 20}},
			i:                5,
			towardsZero:      false,
			wantClosestIndex: 10,
		},
		{
			ranges:           [][2]int64{{10, 20}},
			i:                25,
			towardsZero:      false,
			wantClosestIndex: -1,
		},
		{
			ranges:           [][2]int64{{10, 20}},
			i:                25,
			towardsZero:      true,
			wantClosestIndex: 20,
		},
		{
			ranges:           [][2]int64{{10, 20}, {30, 40}},
			i:                25,
			towardsZero:      true,
			wantClosestIndex: 20,
		},
		{
			ranges:           [][2]int64{{10, 20}, {30, 40}},
			i:                25,
			towardsZero:      false,
			wantClosestIndex: 30,
		},
		{
			ranges:           [][2]int64{{10, 20}, {30, 40}},
			i:                35,
			towardsZero:      true,
			wantClosestIndex: 30,
		},
		{
			ranges:           [][2]int64{{10, 20}, {30, 40}},
			i:                35,
			towardsZero:      false,
			wantClosestIndex: 40,
		},
		{
			ranges:           [][2]int64{{0, 0}},
			i:                0,
			towardsZero:      true,
			wantClosestIndex: 0,
		},
		{
			ranges:           [][2]int64{{0, 0}},
			i:                0,
			towardsZero:      false,
			wantClosestIndex: 0,
		},
		{
			ranges:           [][2]int64{{10, 10}},
			i:                10,
			towardsZero:      false,
			wantClosestIndex: 10,
		},
		{
			ranges:           [][2]int64{{10, 10}},
			i:                10,
			towardsZero:      true,
			wantClosestIndex: 10,
		},
		{
			ranges:           [][2]int64{{20, 40}},
			i:                40,
			towardsZero:      true,
			wantClosestIndex: 20,
		},
		{
			ranges:           [][2]int64{{0, 20}, {40, 60}},
			i:                40,
			towardsZero:      true,
			wantClosestIndex: 40,
		},
		{
			ranges:           [][2]int64{{0, 20}, {40, 60}},
			i:                20,
			towardsZero:      false,
			wantClosestIndex: 20,
		},
	}
	for _, tc := range testCases {
		gotClosest := tc.ranges.ClosestInDirection(tc.i, tc.towardsZero)
		if gotClosest != tc.wantClosestIndex {
			t.Errorf("%+v: got %v want %v", tc, gotClosest, tc.wantClosestIndex)
		}
	}
}

func TestRangeDelta(t *testing.T) {
	testCases := []struct {
		oldRange    SliceRanges
		newRange    SliceRanges
		wantAdded   SliceRanges
		wantSames   SliceRanges
		wantRemoved SliceRanges
	}{
		// added
		{
			oldRange: SliceRanges([][2]int64{}),
			newRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			wantAdded: SliceRanges([][2]int64{
				{0, 9},
			}),
		},
		// removed
		{
			oldRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			newRange: SliceRanges([][2]int64{}),
			wantRemoved: SliceRanges([][2]int64{
				{0, 9},
			}),
		},
		// same
		{
			oldRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			newRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			wantSames: SliceRanges([][2]int64{
				{0, 9},
			}),
		},
		// typical range increase
		{
			oldRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			newRange: SliceRanges([][2]int64{
				{0, 9}, {10, 19},
			}),
			wantSames: SliceRanges([][2]int64{
				{0, 9},
			}),
			wantAdded: SliceRanges([][2]int64{
				{10, 19},
			}),
		},
		// typical range swap
		{
			oldRange: SliceRanges([][2]int64{
				{0, 9}, {10, 19},
			}),
			newRange: SliceRanges([][2]int64{
				{0, 9}, {20, 29},
			}),
			wantSames: SliceRanges([][2]int64{
				{0, 9},
			}),
			wantAdded: SliceRanges([][2]int64{
				{20, 29},
			}),
			wantRemoved: SliceRanges([][2]int64{
				{10, 19},
			}),
		},
		// overlaps are handled intelligently
		{
			oldRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			newRange: SliceRanges([][2]int64{
				{0, 10},
			}),
			wantSames: SliceRanges([][2]int64{
				{0, 9},
			}),
			wantAdded: SliceRanges([][2]int64{
				{10, 10},
			}),
			wantRemoved: [][2]int64{},
		},
		{
			oldRange: SliceRanges([][2]int64{
				{0, 9},
			}),
			newRange: SliceRanges([][2]int64{
				{5, 15},
			}),
			wantSames: SliceRanges([][2]int64{
				{5, 9},
			}),
			wantAdded: SliceRanges([][2]int64{
				{10, 15},
			}),
			wantRemoved: SliceRanges([][2]int64{
				{0, 4},
			}),
		},
		{
			oldRange:    [][2]int64{{5, 15}},
			newRange:    [][2]int64{{0, 9}},
			wantSames:   [][2]int64{{5, 9}},
			wantAdded:   [][2]int64{{0, 4}},
			wantRemoved: [][2]int64{{10, 15}},
		},
		{
			oldRange:    [][2]int64{{0, 20}, {35, 45}},
			newRange:    [][2]int64{{0, 20}, {36, 46}},
			wantSames:   [][2]int64{{0, 20}, {36, 45}},
			wantAdded:   [][2]int64{{46, 46}},
			wantRemoved: [][2]int64{{35, 35}},
		},
		{
			oldRange:    [][2]int64{{0, 20}, {35, 45}},
			newRange:    [][2]int64{{0, 20}, {34, 44}},
			wantSames:   [][2]int64{{0, 20}, {35, 44}},
			wantAdded:   [][2]int64{{34, 34}},
			wantRemoved: [][2]int64{{45, 45}},
		},
		// torture window, multiple same, add, remove
		{
			oldRange:    [][2]int64{{10, 20}, {40, 50}, {70, 80}, {100, 110}},
			newRange:    [][2]int64{{0, 15}, {40, 50}, {80, 90}, {111, 120}},
			wantSames:   [][2]int64{{10, 15}, {40, 50}, {80, 80}},
			wantAdded:   [][2]int64{{0, 9}, {81, 90}, {111, 120}},
			wantRemoved: [][2]int64{{16, 20}, {70, 79}, {100, 110}},
		},
		// swallowed range (one getting smaller, one getting bigger)
		{
			oldRange:    [][2]int64{{0, 20}},
			newRange:    [][2]int64{{5, 15}},
			wantSames:   [][2]int64{{5, 15}},
			wantRemoved: [][2]int64{{0, 4}, {16, 20}},
		},
		{
			oldRange:  [][2]int64{{5, 15}},
			newRange:  [][2]int64{{0, 20}},
			wantSames: [][2]int64{{5, 15}},
			wantAdded: [][2]int64{{0, 4}, {16, 20}},
		},
		// regression test
		{
			oldRange:    [][2]int64{{0, 20}, {20, 24}},
			newRange:    [][2]int64{{0, 20}, {20, 24}},
			wantSames:   [][2]int64{{0, 20}, {20, 24}},
			wantAdded:   [][2]int64{},
			wantRemoved: [][2]int64{},
		},
		// another regression test
		{
			oldRange:    [][2]int64{{0, 0}},
			newRange:    [][2]int64{{0, 0}},
			wantSames:   [][2]int64{{0, 0}},
			wantAdded:   [][2]int64{},
			wantRemoved: [][2]int64{},
		},
		// regression test, 1 element overlap
		{
			oldRange:    [][2]int64{{10, 20}},
			newRange:    [][2]int64{{20, 30}},
			wantSames:   [][2]int64{{20, 20}},
			wantAdded:   [][2]int64{{21, 30}},
			wantRemoved: [][2]int64{{10, 19}},
		},
	}
	for _, tc := range testCases {
		gotAdd, gotRm, gotSame := tc.oldRange.Delta(tc.newRange)
		if tc.wantAdded != nil && !reflect.DeepEqual(gotAdd, tc.wantAdded) {
			t.Errorf("%v -> %v got added %+v want %+v", tc.oldRange, tc.newRange, gotAdd, tc.wantAdded)
		}
		if tc.wantRemoved != nil && !reflect.DeepEqual(gotRm, tc.wantRemoved) {
			t.Errorf("%v -> %v got remove %+v want %+v", tc.oldRange, tc.newRange, gotRm, tc.wantRemoved)
		}
		if tc.wantSames != nil && !reflect.DeepEqual(gotSame, tc.wantSames) {
			t.Errorf("%v -> %v got same %+v want %+v", tc.oldRange, tc.newRange, gotSame, tc.wantSames)
		}
	}
}

func TestSortPoints(t *testing.T) {
	testCases := []struct {
		name  string
		input []pointInfo
		want  []pointInfo
	}{
		{
			name: "two element",
			input: []pointInfo{
				{
					x:          5,
					isOldRange: true,
					isOpen:     true,
				},
				{
					x:          2,
					isOldRange: true,
					isOpen:     true,
				},
			},
			want: []pointInfo{
				{
					x:          2,
					isOldRange: true,
					isOpen:     true,
				},
				{
					x:          5,
					isOldRange: true,
					isOpen:     true,
				},
			},
		},
		{
			name: "no dupes, sort by x",
			input: []pointInfo{
				{
					x:          4,
					isOldRange: true,
					isOpen:     true,
				},
				{
					x:          1,
					isOldRange: true,
					isOpen:     false,
				},
				{
					x:          3,
					isOldRange: false,
					isOpen:     true,
				},
				{
					x:          2,
					isOldRange: false,
					isOpen:     false,
				},
			},
			want: []pointInfo{
				{
					x:          1,
					isOldRange: true,
					isOpen:     false,
				},
				{
					x:          2,
					isOldRange: false,
					isOpen:     false,
				},
				{
					x:          3,
					isOldRange: false,
					isOpen:     true,
				},
				{
					x:          4,
					isOldRange: true,
					isOpen:     true,
				},
			},
		},
		{
			name: "all dupes, sort by open(tie=old), close(tie=old)",
			input: []pointInfo{
				{
					x:          4,
					isOldRange: true,
					isOpen:     true,
				},
				{
					x:          4,
					isOldRange: false,
					isOpen:     true,
				},
				{
					x:          4,
					isOldRange: false,
					isOpen:     false,
				},
				{
					x:          4,
					isOldRange: true,
					isOpen:     false,
				},
			},
			want: []pointInfo{
				{
					x:          4,
					isOldRange: true,
					isOpen:     true,
				},
				{
					x:          4,
					isOldRange: false,
					isOpen:     true,
				},
				{
					x:          4,
					isOldRange: true,
					isOpen:     false,
				},
				{
					x:          4,
					isOldRange: false,
					isOpen:     false,
				},
			},
		},
	}
	for _, tc := range testCases {
		sortPoints(tc.input)
		if !reflect.DeepEqual(tc.input, tc.want) {
			t.Errorf("%s: got %+v\nwant %+v", tc.name, tc.input, tc.want)
		}
	}
}
