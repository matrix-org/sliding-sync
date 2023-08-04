package syncv3_test

import (
	"fmt"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"testing"
)

func TestInvitesFromIgnoredUsersOmitted(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	bob := registerNamedUser(t, "bob")
	nigel := registerNamedUser(t, "nigel")

	t.Log("Nigel create two public rooms. Bob joins both.")
	room1 := nigel.CreateRoom(t, map[string]any{"preset": "public_chat", "name": "room 1"})
	room2 := nigel.CreateRoom(t, map[string]any{"preset": "public_chat", "name": "room 2"})
	bob.JoinRoom(t, room1, nil)
	bob.JoinRoom(t, room2, nil)

	t.Log("Alice makes a room for dumping sentinel messages.")
	aliceRoom := alice.CreateRoom(t, map[string]any{"preset": "private_chat"})

	t.Log("Alice ignores Nigel.")
	alice.SetGlobalAccountData(t, "m.ignored_user_list", map[string]any{
		"ignored_users": map[string]any{
			nigel.UserID: map[string]any{},
		},
	})

	t.Log("Nigel invites Alice to room 1.")
	nigel.InviteRoom(t, room1, alice.UserID)

	t.Log("Bob sliding syncs until he sees that invite.")
	bob.SlidingSyncUntilMembership(t, "", room1, alice, "invite")

	t.Log("Alice sends a sentinel message in her private room.")
	sentinel := alice.SendEventSynced(t, aliceRoom, Event{
		Type: "m.room.message",
		Content: map[string]any{
			"body":    "Hello, world!",
			"msgtype": "m.text",
		},
	})

	t.Log("Alice does an initial sliding sync.")
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 20,
				},
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	})

	t.Log("Alice should see her sentinel, but not Nigel's invite.")
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(
		map[string][]m.RoomMatcher{
			aliceRoom: {MatchRoomTimelineMostRecent(1, []Event{{ID: sentinel}})},
		},
	))

	t.Log("Nigel invites Alice to room 2.")
	nigel.InviteRoom(t, room2, alice.UserID)

	t.Log("Bob sliding syncs until he sees that invite.")
	bob.SlidingSyncUntilMembership(t, "", room1, alice, "invite")

	t.Log("Alice sends a sentinel message in her private room.")
	sentinel = alice.SendEventSynced(t, aliceRoom, Event{
		Type: "m.room.message",
		Content: map[string]any{
			"body":    "Hello, world, again",
			"msgtype": "m.text",
		},
	})

	t.Log("Alice does an incremental sliding sync.")
	res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, func(response *sync3.Response) error {
		if m.MatchRoomSubscription(room2)(response) == nil {
			err := fmt.Errorf("unexpectedly got subscription for room 2 (%s)", room2)
			t.Error(err)
			return err
		}

		gotSentinel := m.MatchRoomSubscription(aliceRoom, MatchRoomTimelineMostRecent(1, []Event{{ID: sentinel}}))
		return gotSentinel(response)
	})

}
