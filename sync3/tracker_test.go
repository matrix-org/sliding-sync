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
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser("alice"), []string{"room1", "room2"})
	assertEqualSlices(t, "", jrt.JoinedUsersForRoom("room1"), []string{"alice"})
	jrt.UserLeftRoom("alice", "room1")
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser("alice"), []string{"room2"})
	assertEqualSlices(t, "", jrt.JoinedUsersForRoom("room2"), []string{"alice", "bob"})

	// bogus values
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser("unknown"), nil)
	assertEqualSlices(t, "", jrt.JoinedUsersForRoom("unknown"), nil)

	// leaves before joins
	jrt.UserLeftRoom("alice", "unknown")
	jrt.UserLeftRoom("unknown", "unknown2")
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser("alice"), []string{"room2"})

	jrt.UserInvitedToRoom("alice", "room4")
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 1)
	jrt.UserJoinedRoom("alice", "room4")
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 0)
	jrt.UserJoinedRoom("alice", "room4") // dupe joins don't bother it
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 0)
	jrt.UserInvitedToRoom("bob", "room4")
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 1)
	jrt.UserInvitedToRoom("bob", "room4") // dupe invites don't bother it
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 1)
	jrt.UserLeftRoom("bob", "room4")
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 0)
}

func assertNumEquals(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("wrong number: got %v want %v", got, want)
	}
}

func assertEqualSlices(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: slices not equal, length mismatch: got %v , want %v", name, got, want)
	}
	sort.Strings(got)
	sort.Strings(want)
	for i := 0; i < len(got); i++ {
		if got[i] != want[i] {
			t.Errorf("%s: slices not equal, got %v want %v", name, got, want)
		}
	}
}
