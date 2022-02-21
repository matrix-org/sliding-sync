package syncv3

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
)

func TestRoomStateTransitions(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	alice := "@alice:localhost"
	bob := "@bob:localhost"
	aliceToken := "ALICE_BEARER_TOKEN"
	bobToken := "BOB_BEARER_TOKEN"
	// make 4 rooms, last room is most recent, with various membership states for Bob
	latestTimestamp := time.Now()
	indexBobJoined := 0
	indexBobKicked := 1
	indexBobBanned := 2
	indexBobInvited := 3
	allRoomsAlicePerspective := []roomEvents{
		{
			roomID: "!TestRoomStateTransitions_joined:localhost",
			events: append(createRoomState(t, alice, latestTimestamp), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join", "displayname": "Bob"}, testutils.WithTimestamp(latestTimestamp.Add(-6*time.Second))),
			}...),
		},
		{
			roomID: "!TestRoomStateTransitions_kicked:localhost",
			events: append(createRoomState(t, alice, latestTimestamp), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}, testutils.WithTimestamp(latestTimestamp.Add(-5*time.Second))),
				testutils.NewStateEvent(t, "m.room.member", bob, alice, map[string]interface{}{"membership": "leave"}, testutils.WithTimestamp(latestTimestamp.Add(-4*time.Second))),
			}...),
		},
		{
			roomID: "!TestRoomStateTransitions_banned:localhost",
			events: append(createRoomState(t, alice, latestTimestamp), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}, testutils.WithTimestamp(latestTimestamp.Add(-3*time.Second))),
				testutils.NewStateEvent(t, "m.room.member", bob, alice, map[string]interface{}{"membership": "ban"}, testutils.WithTimestamp(latestTimestamp.Add(-2*time.Second))),
			}...),
		},
		{
			roomID: "!TestRoomStateTransitions_invited:localhost",
			events: append(createRoomState(t, alice, latestTimestamp), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.member", bob, alice, map[string]interface{}{"membership": "invite"}, testutils.WithTimestamp(latestTimestamp)),
			}...),
		},
	}
	v2.addAccount(alice, aliceToken)
	v2.addAccount(bob, bobToken)
	/*
		v2.queueResponse(alice, sync2.SyncResponse{
			Rooms: sync2.SyncRoomsResponse{
				Join: v2JoinTimeline(allRoomsAlicePerspective...),
			},
		}) */
	allRoomsBobPerspective := []roomEvents{
		allRoomsAlicePerspective[indexBobJoined],
		allRoomsAlicePerspective[indexBobKicked],
		allRoomsAlicePerspective[indexBobBanned],
	}
	// The stripped state does not include a timestamp so it will be treated as most recent hence the invite room must come last in allRoomsAlicePerspective
	var inviteStrippedState sync2.SyncV2InviteResponse
	inviteStrippedState.InviteState.Events = []json.RawMessage{
		testutils.NewStateEvent(t, "m.room.member", bob, alice, map[string]interface{}{"membership": "invite"}),
	}
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRoomsBobPerspective...),
			Invite: map[string]sync2.SyncV2InviteResponse{
				allRoomsAlicePerspective[indexBobInvited].roomID: inviteStrippedState,
			},
		},
	})

	// bob should see the invited/joined rooms
	res := v3.mustDoV3Request(t, bobToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
				Sort: []string{sync3.SortByRecency},
			},
		},
	})
	MatchResponse(t, res, MatchV3Count(2), MatchV3Ops(
		MatchV3SyncOpWithMatchers(
			MatchRoomRange([]roomMatcher{
				MatchRoomID(allRoomsAlicePerspective[indexBobInvited].roomID),
			}, []roomMatcher{
				MatchRoomID(allRoomsAlicePerspective[indexBobJoined].roomID),
			}),
		),
	))

}
