package synclive

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
	}

	for _, tc := range testCases {
		result := tc.input.SliceInto(stringSlice(alphabet))
		if len(result) != len(tc.want) {
			t.Errorf("%+v subslice mismatch: got %v want %v", tc, len(result), len(tc.want))
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
	}
	for _, tc := range testCases {
		gotAdd, gotRm, gotSame := tc.oldRange.Delta(tc.newRange)
		if tc.wantAdded != nil && !reflect.DeepEqual(gotAdd, tc.wantAdded) {
			t.Errorf("%+v got added %+v", tc, gotAdd)
		}
		if tc.wantRemoved != nil && !reflect.DeepEqual(gotRm, tc.wantRemoved) {
			t.Errorf("%+v got removed %+v", tc, gotRm)
		}
		if tc.wantSames != nil && !reflect.DeepEqual(gotSame, tc.wantSames) {
			t.Errorf("%+v got sames %+v", tc, gotSame)
		}
	}
}
