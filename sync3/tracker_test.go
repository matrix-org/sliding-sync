package sync3

import (
	"sort"
	"testing"
)

func joinedUsersForRoom(jrt *JoinedRoomsTracker, roomID string) []string {
	users, _ := jrt.JoinedUsersForRoom(roomID, nil)
	return users
}

func TestTracker(t *testing.T) {
	// basic usage
	jrt := NewJoinedRoomsTracker()
	jrt.UserJoinedRoom("alice", "room1")
	jrt.UserJoinedRoom("alice", "room2")
	jrt.UserJoinedRoom("bob", "room2")
	jrt.UserJoinedRoom("bob", "room3")
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser("alice"), []string{"room1", "room2"})
	assertEqualSlices(t, "", joinedUsersForRoom(jrt, "room1"), []string{"alice"})
	jrt.UserLeftRoom("alice", "room1")
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser("alice"), []string{"room2"})
	assertEqualSlices(t, "", joinedUsersForRoom(jrt, "room2"), []string{"alice", "bob"})
	// test filters
	fUsers, joinCount := jrt.JoinedUsersForRoom("room2", func(userID string) bool {
		return userID == "alice"
	})
	assertInt(t, joinCount, 2)
	assertEqualSlices(t, "filter_users wrong", fUsers, []string{"alice"})
	fUsers, joinCount = jrt.JoinedUsersForRoom("room2", func(userID string) bool {
		return userID == "unmatched"
	})
	assertInt(t, joinCount, 2)
	assertEqualSlices(t, "wanted no filtered users", fUsers, nil)

	// bogus values
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser("unknown"), nil)
	assertEqualSlices(t, "", joinedUsersForRoom(jrt, "unknown"), nil)

	// leaves before joins
	jrt.UserLeftRoom("alice", "unknown")
	jrt.UserLeftRoom("unknown", "unknown2")
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser("alice"), []string{"room2"})

	jrt.UsersInvitedToRoom([]string{"alice"}, "room4")
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 1)
	jrt.UserJoinedRoom("alice", "room4")
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 0)
	jrt.UserJoinedRoom("alice", "room4") // dupe joins don't bother it
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 0)
	jrt.UsersInvitedToRoom([]string{"bob"}, "room4")
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 1)
	jrt.UsersInvitedToRoom([]string{"bob"}, "room4") // dupe invites don't bother it
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 1)
	jrt.UserLeftRoom("bob", "room4")
	assertNumEquals(t, jrt.NumInvitedUsersForRoom("room4"), 0)
}

func TestTrackerStartup(t *testing.T) {
	roomA := "!a"
	roomB := "!b"
	roomC := "!c"
	alice := "@alice"
	bob := "@bob"
	jrt := NewJoinedRoomsTracker()
	jrt.Startup(map[string][]string{
		roomA: {alice, bob},
		roomB: {bob},
		roomC: {alice},
	})
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser(alice), []string{roomA, roomC})
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser(bob), []string{roomA, roomB})
	assertEqualSlices(t, "", jrt.JoinedRoomsForUser("@someone"), []string{})
	assertBool(t, "alice should be joined", jrt.IsUserJoined(alice, roomA), true)
	assertBool(t, "alice should not be joined", jrt.IsUserJoined(alice, roomB), false)
	assertBool(t, "alice should be joined", jrt.IsUserJoined(alice, roomC), true)
	assertBool(t, "bob should be joined", jrt.IsUserJoined(bob, roomA), true)
	assertBool(t, "bob should be joined", jrt.IsUserJoined(bob, roomB), true)
	assertBool(t, "bob should not be joined", jrt.IsUserJoined(bob, roomC), false)
	assertInt(t, jrt.NumInvitedUsersForRoom(roomA), 0)
	assertInt(t, jrt.NumInvitedUsersForRoom(roomB), 0)
	assertInt(t, jrt.NumInvitedUsersForRoom(roomC), 0)
}

func assertBool(t *testing.T, msg string, got, want bool) {
	t.Helper()
	if got != want {
		t.Errorf(msg)
	}
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
