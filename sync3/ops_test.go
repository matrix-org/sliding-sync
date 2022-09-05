package sync3

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/matrix-org/sync-v3/testutils"
)

func TestCalculateListOps_BasicOperations(t *testing.T) {
	testCases := []struct {
		name     string
		before   []string
		after    []string
		ranges   SliceRanges
		roomID   string
		listOp   ListOp
		wantOps  []ResponseOp
		wantSubs []string
	}{
		{
			name:   "basic addition from nothing",
			before: []string{},
			after:  []string{"a"},
			ranges: SliceRanges{{0, 20}},
			roomID: "a",
			listOp: ListOpAdd,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(0)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(0), RoomID: "a"},
			},
			wantSubs: []string{"a"},
		},
		{
			name:   "basic deletion to nothing",
			before: []string{"a"},
			after:  []string{},
			ranges: SliceRanges{{0, 20}},
			roomID: "a",
			listOp: ListOpDel,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(0)},
			},
		},
		{
			name:   "basic move from bottom to top of list",
			before: []string{"a", "b", "c", "d"},
			after:  []string{"d", "a", "b", "c"},
			ranges: SliceRanges{{0, 20}},
			roomID: "d",
			listOp: ListOpChange, // e.g new message
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(3)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(0), RoomID: "d"},
			},
		},
		{
			name:   "basic move from top to bottom of list",
			before: []string{"a", "b", "c", "d"},
			after:  []string{"b", "c", "d", "a"},
			ranges: SliceRanges{{0, 20}},
			roomID: "a",
			listOp: ListOpChange, // e.g new message
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(0)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(3), RoomID: "a"},
			},
		},
		{
			name:    "no-op move",
			before:  []string{"a", "b", "c", "d"},
			after:   []string{"a", "b", "c", "d"},
			ranges:  SliceRanges{{0, 20}},
			roomID:  "b",
			listOp:  ListOpChange, // e.g new message
			wantOps: nil,
		},
		{
			name:   "basic addition to middle of list",
			before: []string{"a", "b", "c", "d"},
			after:  []string{"a", "b", "bc", "c", "d"},
			ranges: SliceRanges{{0, 20}},
			roomID: "bc",
			listOp: ListOpAdd,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(4)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(2), RoomID: "bc"},
			},
			wantSubs: []string{"bc"},
		},
		{
			name:   "basic deletion from  middle of list",
			before: []string{"a", "b", "c", "d", "e"},
			after:  []string{"a", "c", "d", "e"},
			ranges: SliceRanges{{0, 20}},
			roomID: "b",
			listOp: ListOpDel,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(1)},
			},
		},
		{
			name:   "basic deletion from end of list",
			before: []string{"a", "b", "c", "d"},
			after:  []string{"a", "b", "c"},
			ranges: SliceRanges{{0, 20}},
			roomID: "d",
			listOp: ListOpDel,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(3)},
			},
		},
		{
			name:   "basic addition to end of list",
			before: []string{"a", "b", "c"},
			after:  []string{"a", "b", "c", "d"},
			ranges: SliceRanges{{0, 20}},
			roomID: "d",
			listOp: ListOpAdd,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(3)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(3), RoomID: "d"},
			},
			wantSubs: []string{"d"},
		},
		{
			name:   "one element swap",
			before: []string{"a", "b", "c", "d"},
			after:  []string{"b", "a", "c", "d"},
			ranges: SliceRanges{{0, 20}},
			roomID: "b",
			listOp: ListOpChange, // e.g new message
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(1)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(0), RoomID: "b"},
			},
		},
	}
	for _, tc := range testCases {
		sl := newStringList(tc.before)
		sl.sortedRoomIDs = tc.after
		gotOps, gotSubs := CalculateListOps(&RequestList{
			Ranges: tc.ranges,
		}, sl, tc.roomID, tc.listOp)
		assertEqualOps(t, tc.name, gotOps, tc.wantOps)
		assertEqualSlices(t, tc.name, gotSubs, tc.wantSubs)
	}
}

func TestCalculateListOps_SingleWindowOperations(t *testing.T) {
	testCases := []struct {
		name     string
		before   []string
		after    []string
		ranges   SliceRanges
		roomID   string
		listOp   ListOp
		wantOps  []ResponseOp
		wantSubs []string
	}{
		{
			name:   "basic move into a window",
			before: []string{"a", "b", "c", "d", "e", "f"},
			after:  []string{"f", "a", "b", "c", "d", "e"},
			ranges: SliceRanges{{0, 2}},
			roomID: "f",
			listOp: ListOpChange,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(2)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(0), RoomID: "f"},
			},
			wantSubs: []string{"f"},
		},
		{
			name:   "basic move out of a window",
			before: []string{"a", "b", "c", "d", "e", "f"},
			after:  []string{"b", "c", "d", "e", "a", "f"},
			ranges: SliceRanges{{0, 2}},
			roomID: "a",
			listOp: ListOpChange,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(0)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(2), RoomID: "d"},
			},
			wantSubs: []string{"d"},
		},
		{
			name:   "basic move over a window",
			before: []string{"a", "b", "c", "d", "e", "f"},
			after:  []string{"b", "c", "d", "e", "a", "f"},
			ranges: SliceRanges{{1, 3}},
			roomID: "a",
			listOp: ListOpChange,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(1)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(3), RoomID: "e"},
			},
			wantSubs: []string{"e"},
		},
		{
			name:   "basic deletion in front of a window",
			before: []string{"a", "b", "c", "d", "e", "f"},
			after:  []string{"b", "c", "d", "e", "f"},
			ranges: SliceRanges{{1, 3}},
			roomID: "a",
			listOp: ListOpDel,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(1)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(3), RoomID: "e"},
			},
			wantSubs: []string{"e"},
		},
		{
			name:   "basic deletion at start of a window",
			before: []string{"a", "b", "c", "d", "e", "f"},
			after:  []string{"a", "c", "d", "e", "f"},
			ranges: SliceRanges{{1, 3}},
			roomID: "b",
			listOp: ListOpDel,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(1)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(3), RoomID: "e"},
			},
			wantSubs: []string{"e"},
		},
		{
			name:   "basic deletion at end of a window",
			before: []string{"a", "b", "c", "d", "e", "f"},
			after:  []string{"a", "b", "c", "e", "f"},
			ranges: SliceRanges{{1, 3}},
			roomID: "d",
			listOp: ListOpDel,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(3)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(3), RoomID: "e"},
			},
			wantSubs: []string{"e"},
		},
		{
			name:     "basic deletion in behind a window",
			before:   []string{"a", "b", "c", "d", "e", "f"},
			after:    []string{"a", "b", "c", "d", "f"},
			ranges:   SliceRanges{{1, 3}},
			roomID:   "e",
			listOp:   ListOpDel,
			wantOps:  nil,
			wantSubs: nil,
		},
		{
			name:   "basic addition in front of a window",
			before: []string{"a", "b", "c", "d", "e", "f"},
			after:  []string{"a", "ab", "b", "c", "d", "e", "f"},
			ranges: SliceRanges{{2, 4}},
			roomID: "ab",
			listOp: ListOpAdd,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(4)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(2), RoomID: "b"},
			},
			wantSubs: []string{"b"},
		},
		{
			name:   "basic addition at start of a window",
			before: []string{"a", "b", "c", "d", "e", "f"},
			after:  []string{"a", "ab", "b", "c", "d", "e", "f"},
			ranges: SliceRanges{{1, 3}},
			roomID: "ab",
			listOp: ListOpAdd,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(3)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(1), RoomID: "ab"},
			},
			wantSubs: []string{"ab"},
		},
		{
			name:   "basic addition at end of a window",
			before: []string{"a", "b", "c", "d", "e", "f"},
			after:  []string{"a", "b", "c", "cd", "d", "e", "f"},
			ranges: SliceRanges{{1, 3}},
			roomID: "cd",
			listOp: ListOpAdd,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(3)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(3), RoomID: "cd"},
			},
			wantSubs: []string{"cd"},
		},
		{
			name:     "basic addition behind a window",
			before:   []string{"a", "b", "c", "d", "e", "f"},
			after:    []string{"a", "b", "c", "d", "e", "f", "g"},
			ranges:   SliceRanges{{2, 4}},
			roomID:   "g",
			listOp:   ListOpAdd,
			wantOps:  nil,
			wantSubs: nil,
		},
	}
	for _, tc := range testCases {
		sl := newStringList(tc.before)
		sl.sortedRoomIDs = tc.after
		gotOps, gotSubs := CalculateListOps(&RequestList{
			Ranges: tc.ranges,
		}, sl, tc.roomID, tc.listOp)
		assertEqualOps(t, tc.name, gotOps, tc.wantOps)
		assertEqualSlices(t, tc.name, gotSubs, tc.wantSubs)
	}
}

func TestCalculateListOps_MultipleWindowOperations(t *testing.T) {
	testCases := []struct {
		name     string
		before   []string
		after    []string
		ranges   SliceRanges
		roomID   string
		listOp   ListOp
		wantOps  []ResponseOp
		wantSubs []string
	}{
		{
			name:   "move within lower window",
			before: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			after:  []string{"a", "c", "b", "d", "e", "f", "g", "h", "i"},
			ranges: SliceRanges{{0, 2}, {5, 7}},
			roomID: "c",
			listOp: ListOpChange,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(2)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(1), RoomID: "c"},
			},
		},
		{
			name:   "move within upper window",
			before: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			after:  []string{"a", "b", "c", "d", "e", "f", "h", "g", "i"},
			ranges: SliceRanges{{0, 2}, {5, 7}},
			roomID: "h",
			listOp: ListOpChange,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(7)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(6), RoomID: "h"},
			},
		},
		{
			name:    "move in the gap between windows",
			before:  []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			after:   []string{"a", "b", "c", "e", "d", "f", "g", "h", "i"},
			ranges:  SliceRanges{{0, 2}, {5, 7}},
			roomID:  "e",
			listOp:  ListOpChange,
			wantOps: nil,
		},
		{
			name: "move from upper window to lower window",
			//                0    1    2    3    4    5    6    7    8
			before: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			after:  []string{"a", "g", "b", "c", "d", "e", "f", "h", "i"},
			ranges: SliceRanges{{0, 2}, {5, 7}},
			roomID: "g",
			listOp: ListOpChange,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(6)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(5), RoomID: "e"},
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(2)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(1), RoomID: "g"},
			},
			wantSubs: []string{"e"},
		},
		{
			name: "move from lower window to upper window",
			//                0    1    2    3    4    5    6    7    8
			before: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			after:  []string{"a", "c", "d", "e", "f", "g", "b", "h", "i"},
			ranges: SliceRanges{{0, 2}, {5, 7}},
			roomID: "b",
			listOp: ListOpChange,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(1)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(2), RoomID: "d"},
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(5)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(6), RoomID: "b"},
			},
			wantSubs: []string{"d"},
		},
		{
			name: "jump over multiple windows to lower",
			//                0    1    2    3    4    5    6    7    8
			before: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			after:  []string{"i", "a", "b", "c", "d", "e", "f", "g", "h"},
			ranges: SliceRanges{{1, 3}, {5, 7}},
			roomID: "i",
			listOp: ListOpChange,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(3)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(1), RoomID: "a"},
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(7)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(5), RoomID: "e"},
			},
			wantSubs: []string{"a", "e"},
		},
		{
			name: "jump over multiple windows to upper",
			//                0    1    2    3    4    5    6    7    8
			before: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			after:  []string{"b", "c", "d", "e", "f", "g", "h", "i", "a"},
			ranges: SliceRanges{{1, 3}, {5, 7}},
			roomID: "a",
			listOp: ListOpChange,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(1)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(3), RoomID: "e"},
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(5)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(7), RoomID: "i"},
			},
			wantSubs: []string{"i", "e"},
		},
		{
			name: "jump from upper window over lower window",
			//                0    1    2    3    4    5    6    7    8
			before: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			after:  []string{"g", "a", "b", "c", "d", "e", "f", "h", "i"},
			ranges: SliceRanges{{1, 3}, {5, 7}},
			roomID: "g",
			listOp: ListOpChange,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(6)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(5), RoomID: "e"},
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(3)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(1), RoomID: "a"},
			},
			wantSubs: []string{"a", "e"},
		},
		{
			name: "addition moving multiple windows",
			//                0    1    2    3    4    5    6    7    8
			before: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			after:  []string{"_", "a", "b", "c", "d", "e", "f", "g", "h", "i"},
			ranges: SliceRanges{{1, 3}, {5, 7}},
			roomID: "_",
			listOp: ListOpAdd,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(3)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(1), RoomID: "a"},
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(7)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(5), RoomID: "e"},
			},
			wantSubs: []string{"a", "e"},
		},
		{
			name: "deletion moving multiple windows",
			//                0    1    2    3    4    5    6    7    8
			before: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
			after:  []string{"b", "c", "d", "e", "f", "g", "h", "i", "j"},
			ranges: SliceRanges{{1, 3}, {5, 7}},
			roomID: "a",
			listOp: ListOpDel,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(1)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(3), RoomID: "e"},
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(5)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(7), RoomID: "i"},
			},
			wantSubs: []string{"i", "e"},
		},
		{
			name: "deletion moving multiple windows window matches end of list",
			//                0    1    2    3    4    5    6    7    8
			before: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			after:  []string{"b", "c", "d", "e", "f", "g", "h", "i"},
			ranges: SliceRanges{{1, 3}, {5, 7}},
			roomID: "a",
			listOp: ListOpDel,
			wantOps: []ResponseOp{
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(5)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(7), RoomID: "i"},
				&ResponseOpSingle{Operation: OpDelete, Index: ptr(1)},
				&ResponseOpSingle{Operation: OpInsert, Index: ptr(3), RoomID: "e"},
			},
			wantSubs: []string{"i", "e"},
		},
	}
	for _, tc := range testCases {
		sl := newStringList(tc.before)
		sl.sortedRoomIDs = tc.after
		gotOps, gotSubs := CalculateListOps(&RequestList{
			Ranges: tc.ranges,
		}, sl, tc.roomID, tc.listOp)
		assertEqualOps(t, tc.name, gotOps, tc.wantOps)
		assertEqualSlices(t, tc.name, gotSubs, tc.wantSubs)
	}
}

func TestCalculateListOps_TortureSingleWindow_Move(t *testing.T) {
	rand.Seed(42)
	ranges := SliceRanges{{0, 5}}
	inRangeIDs := map[string]bool{
		"a": true, "b": true, "c": true, "d": true, "e": true, "f": true,
	}
	for i := 0; i < 10000; i++ {
		before := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		after, roomID, fromIndex, toIndex := testutils.MoveRandomElement(before)
		t.Logf("move %s from %v to %v -> %v", roomID, fromIndex, toIndex, after)

		sl := newStringList(before)
		sl.sortedRoomIDs = after
		gotOps, gotSubs := CalculateListOps(&RequestList{
			Ranges: ranges,
		}, sl, roomID, ListOpChange)

		for _, sub := range gotSubs {
			if inRangeIDs[sub] {
				t.Errorf("CalculateListOps: got sub %v but it was already in the range", sub)
			}
		}
		if int64(fromIndex) > ranges[0][1] && int64(toIndex) > ranges[0][1] {
			// the swap happens outside the range, so we expect nothing
			if len(gotOps) != 0 {
				t.Errorf("CalculateLisOps: swap outside range got ops wanted none: %+v", gotOps)
			}
			continue
		}
		if fromIndex == toIndex {
			// we want 0 ops
			if len(gotOps) > 0 {
				t.Errorf("CalculateLisOps: from==to got ops wanted none: %+v", gotOps)
			}
			continue
		}
		if len(gotOps) != 2 {
			t.Fatalf("CalculateLisOps: wanted 2 ops, got %+v", gotOps)
			continue
		}
		if int64(fromIndex) > ranges[0][1] {
			// should inject a different room at the start of the range
			fromIndex = int(ranges[0][1])
		}
		assertSingleOp(t, gotOps[0], "DELETE", fromIndex, "")
		if int64(toIndex) > ranges[0][1] {
			// should inject a different room at the end of the range
			toIndex = int(ranges[0][1])
			roomID = after[toIndex]
		}
		if !inRangeIDs[roomID] {
			// it should now be in-range
			inRange := false
			for _, sub := range gotSubs {
				if sub == roomID {
					inRange = true
					break
				}
			}
			if !inRange {
				t.Errorf("got subs %v missing room %v", gotSubs, roomID)
			}
		}
		assertSingleOp(t, gotOps[1], "INSERT", toIndex, roomID)
	}
}

func TestCalculateListOps_TortureSingleWindowMiddle_Move(t *testing.T) {
	rand.Seed(41)
	ranges := SliceRanges{{2, 5}}
	inRangeIDs := map[string]bool{
		"c": true, "d": true, "e": true, "f": true,
	}
	for i := 0; i < 10000; i++ {
		before := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		after, roomID, fromIndex, toIndex := testutils.MoveRandomElement(before)
		t.Logf("move %s from %v to %v -> %v", roomID, fromIndex, toIndex, after)

		sl := newStringList(before)
		sl.sortedRoomIDs = after
		gotOps, gotSubs := CalculateListOps(&RequestList{
			Ranges: ranges,
		}, sl, roomID, ListOpChange)

		for _, sub := range gotSubs {
			if inRangeIDs[sub] {
				t.Errorf("CalculateListOps: got sub %v but it was already in the range", sub)
			}
		}
		if (int64(fromIndex) > ranges[0][1] && int64(toIndex) > ranges[0][1]) || (int64(fromIndex) < ranges[0][0] && int64(toIndex) < ranges[0][0]) {
			// the swap happens outside the range, so we expect nothing
			if len(gotOps) != 0 {
				t.Errorf("CalculateLisOps: swap outside range got ops wanted none: %+v", gotOps)
			}
			continue
		}
		if fromIndex == toIndex {
			// we want 0 ops
			if len(gotOps) > 0 {
				t.Errorf("CalculateLisOps: from==to got ops wanted none: %+v", gotOps)
			}
			continue
		}
		if len(gotOps) != 2 {
			t.Fatalf("CalculateListOps: wanted 2 ops, got %+v", gotOps)
			continue
		}
		if int64(fromIndex) > ranges[0][1] {
			// should inject a different room at the start of the range
			fromIndex = int(ranges[0][1])
		} else if int64(fromIndex) < ranges[0][0] {
			// should inject a different room at the start of the range
			fromIndex = int(ranges[0][0])
		}
		assertSingleOp(t, gotOps[0], "DELETE", fromIndex, "")
		if int64(toIndex) > ranges[0][1] {
			// should inject a different room at the end of the range
			toIndex = int(ranges[0][1])
			roomID = after[toIndex]
		} else if int64(toIndex) < ranges[0][0] {
			// should inject a different room at the end of the range
			toIndex = int(ranges[0][0])
			roomID = after[toIndex]
		}
		if !inRangeIDs[roomID] {
			// it should now be in-range
			inRange := false
			for _, sub := range gotSubs {
				if sub == roomID {
					inRange = true
					break
				}
			}
			if !inRange {
				t.Errorf("got subs %v missing room %v", gotSubs, roomID)
			}
		}
		assertSingleOp(t, gotOps[1], "INSERT", toIndex, roomID)
	}
}

func assertSingleOp(t *testing.T, op ResponseOp, opName string, index int, optRoomID string) {
	t.Helper()
	singleOp, ok := op.(*ResponseOpSingle)
	if !ok {
		t.Errorf("op was not a single operation, got %+v", op)
	}
	if singleOp.Operation != opName {
		t.Errorf("op name got %v want %v", singleOp.Operation, opName)
	}
	if *singleOp.Index != index {
		t.Errorf("op index got %v want %v", *singleOp.Index, index)
	}
	if optRoomID != "" && singleOp.RoomID != optRoomID {
		t.Errorf("op room ID got %v want %v", singleOp.RoomID, optRoomID)
	}
}

func assertEqualOps(t *testing.T, name string, gotOps, wantOps []ResponseOp) {
	t.Helper()
	got, err := json.Marshal(gotOps)
	want, err2 := json.Marshal(wantOps)
	if err != nil || err2 != nil {
		t.Fatalf("%s: failed to marshal response ops: %v %v", name, err, err2)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: assertEqualOps, got != want\n%s\n%s", name, string(got), string(want))
	}
}

type stringList struct {
	roomIDs       []string
	roomIDToIndex map[string]int
	sortedRoomIDs []string
}

func newStringList(roomIDs []string) *stringList {
	s := &stringList{
		sortedRoomIDs: roomIDs,
	}
	s.Sort(nil)
	return s
}

func ptr(i int) *int {
	return &i
}

func (s *stringList) IndexOf(roomID string) (int, bool) {
	i, ok := s.roomIDToIndex[roomID]
	return i, ok
}
func (s *stringList) Len() int64 {
	return int64(len(s.roomIDs))
}
func (s *stringList) Sort(sortBy []string) error {
	s.roomIDToIndex = make(map[string]int, len(s.sortedRoomIDs))
	s.roomIDs = s.sortedRoomIDs
	for i := range s.roomIDs {
		s.roomIDToIndex[s.roomIDs[i]] = i
	}
	return nil
}
func (s *stringList) Add(roomID string) bool {
	_, ok := s.roomIDToIndex[roomID]
	if ok {
		return false
	}
	s.roomIDToIndex[roomID] = len(s.roomIDs)
	s.roomIDs = append(s.roomIDs, roomID)
	return true
}
func (s *stringList) Remove(roomID string) int {
	i, ok := s.roomIDToIndex[roomID]
	if !ok {
		return -1
	}
	gotRoomID := s.roomIDs[i]
	if gotRoomID != roomID {
		panic("roomIDToIndex|roomIDs out of sync, got " + gotRoomID + " want " + roomID)
	}
	delete(s.roomIDToIndex, roomID)
	// splice out index
	s.roomIDs = append(s.roomIDs[:i], s.roomIDs[i+1:]...)
	// re-update the map
	for index := i; index < len(s.roomIDs); index++ {
		s.roomIDToIndex[s.roomIDs[index]] = index
	}
	return i
}
func (s *stringList) Get(index int) string {
	return s.roomIDs[index]
}
