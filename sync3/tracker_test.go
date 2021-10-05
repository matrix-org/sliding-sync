package sync3

import (
	"sort"
	"testing"
)

func TestTracker(t *testing.T) {
	// basic usage
	jrt := NewJoinedRoomsTracker()
	jrt.UserJoinedRoom("alice", "room1")
	jrt.UserJoinedRoom("alice", "room2")
	jrt.UserJoinedRoom("bob", "room2")
	jrt.UserJoinedRoom("bob", "room3")
	assertEqualSlices(t, jrt.JoinedRoomsForUser("alice"), []string{"room1", "room2"})
	assertEqualSlices(t, jrt.JoinedUsersForRoom("room1"), []string{"alice"})
	jrt.UserLeftRoom("alice", "room1")
	assertEqualSlices(t, jrt.JoinedRoomsForUser("alice"), []string{"room2"})
	assertEqualSlices(t, jrt.JoinedUsersForRoom("room2"), []string{"alice", "bob"})

	// bogus values
	assertEqualSlices(t, jrt.JoinedRoomsForUser("unknown"), nil)
	assertEqualSlices(t, jrt.JoinedUsersForRoom("unknown"), nil)

	// leaves before joins
	jrt.UserLeftRoom("alice", "unknown")
	jrt.UserLeftRoom("unknown", "unknown2")
	assertEqualSlices(t, jrt.JoinedRoomsForUser("alice"), []string{"room2"})
}

func assertEqualSlices(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("slices not equal, length mismatch: got %d , want %d", len(got), len(want))
	}
	sort.Strings(got)
	sort.Strings(want)
	for i := 0; i < len(got); i++ {
		if got[i] != want[i] {
			t.Errorf("slices not equal, got %v want %v", got, want)
		}
	}
}
