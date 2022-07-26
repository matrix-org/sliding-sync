package syncv3_test

import (
	"net/url"
	"testing"

	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils/m"
)

func TestRoomStateTransitions(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	// make 4 rooms and set bob's membership state in each to a different value.
	joinRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	bob.JoinRoom(t, joinRoomID, nil)
	kickRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	bob.JoinRoom(t, kickRoomID, nil)
	alice.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "rooms", kickRoomID, "kick"}, WithJSONBody(t, map[string]interface{}{
		"user_id": bob.UserID,
	}))
	banRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	bob.JoinRoom(t, banRoomID, nil)
	alice.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "rooms", banRoomID, "ban"}, WithJSONBody(t, map[string]interface{}{
		"user_id": bob.UserID,
	}))
	inviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	alice.InviteRoom(t, inviteRoomID, bob.UserID)

	// seed the proxy with Alice data
	alice.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
				Sort: []string{sync3.SortByRecency},
			},
		},
	})

	// bob should see the invited/joined rooms
	bobRes := bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
				Sort: []string{sync3.SortByRecency},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
					RequiredState: [][2]string{
						{"m.room.create", ""},
					},
				},
			},
		},
	})
	m.MatchResponse(t, bobRes, m.MatchList(0, m.MatchV3Count(2), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 100, []string{inviteRoomID, joinRoomID}),
	)), m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
		inviteRoomID: {
			m.MatchRoomHighlightCount(1),
			m.MatchRoomInitial(true),
			m.MatchRoomRequiredState(nil),
			// TODO m.MatchRoomInviteState(inviteStrippedState.InviteState.Events),
		},
		joinRoomID: {},
	}),
	)

	// now bob accepts the invite
	bob.JoinRoom(t, inviteRoomID, nil)

	// the room should be updated with the initial flag set to replace what was in the invite state
	bobRes = bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
			},
		},
	}, WithQueries(url.Values{
		"timeout": []string{"500"},
		"pos":     []string{bobRes.Pos},
	}))
	m.MatchResponse(t, bobRes, m.MatchNoV3Ops(), m.MatchList(0, m.MatchV3Count(2)), m.MatchRoomSubscription(inviteRoomID,
		MatchRoomRequiredState([]Event{
			{
				Type:     "m.room.create",
				StateKey: ptr(""),
			},
		}),
		MatchRoomTimelineMostRecent(1, []Event{
			{
				Type:     "m.room.member",
				StateKey: ptr(bob.UserID),
				Content: map[string]interface{}{
					"membership":  "join",
					"displayname": bob.Localpart,
				},
				Sender: bob.UserID,
			},
		}),
		m.MatchRoomInitial(true),
		m.MatchRoomHighlightCount(0)))

}
