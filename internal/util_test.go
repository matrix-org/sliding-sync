package internal

import (
	"reflect"
	"sort"
	"testing"
)

func TestKeys(t *testing.T) {
	assertSlice(t, Keys((map[string]int)(nil)), nil)
	assertSlice(t, Keys(map[string]int{}), []string{})
	assertSlice(t, Keys(map[string]int{"a": 1}), []string{"a"})
	assertSlice(t, Keys(map[string]int{"a": 1, "b": 2, "c": 3}), []string{"a", "b", "c"})
	assertSlice(t, Keys(map[string]int{"": 1, "!": 1, "☃": 1, "\x00": 1}), []string{"", "!", "☃", "\x00"})
}

// assertSlicesSameElements errors the test if "got" and "want" have different elements.
// Both got and want are sorted in-place as a side effect.
func assertSlice(t *testing.T, got, want []string) {
	if len(got) != len(want) {
		t.Errorf("got length %d, expected length %d", len(got), len(want))
	}

	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })

	if !reflect.DeepEqual(got, want) {
		t.Errorf("After sorting, got %v but expected %v", got, want)
	}
}
