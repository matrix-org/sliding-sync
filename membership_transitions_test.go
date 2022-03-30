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
	bob := "@bob:localhost"
	bobToken := "BOB_BEARER_TOKEN"
	// make 4 rooms, last room is most recent, with various membership states for Bob
	latestTimestamp := time.Now()
	indexBobJoined := 0
	indexBobKicked := 1
	indexBobBanned := 2
	indexBobInvited := 3
	bobInviteEvent := testutils.NewStateEvent(t, "m.room.member", bob, alice, map[string]interface{}{"membership": "invite"}, testutils.WithTimestamp(latestTimestamp))
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
				bobInviteEvent,
			}...),
		},
	}
	v2.addAccount(alice, aliceToken)
	v2.addAccount(bob, bobToken)

	// seed the proxy with Alice data
	_ = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
				Sort: []string{sync3.SortByRecency},
			},
		},
	})

	allRoomsBobPerspective := []roomEvents{
		allRoomsAlicePerspective[indexBobJoined],
		allRoomsAlicePerspective[indexBobKicked],
		allRoomsAlicePerspective[indexBobBanned],
	}

	var inviteStrippedState sync2.SyncV2InviteResponse
	inviteStrippedState.InviteState.Events = []json.RawMessage{bobInviteEvent}
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
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
					RequiredState: [][2]string{
						{"m.room.create", ""},
					},
				},
			},
		},
	})
	MatchResponse(t, res, MatchV3Count(2), MatchV3Ops(
		MatchV3SyncOpWithMatchers(
			MatchRoomRange([]roomMatcher{
				MatchRoomID(allRoomsAlicePerspective[indexBobInvited].roomID),
				MatchRoomHighlightCount(1),
				MatchRoomInitial(true),
				MatchRoomRequiredState(nil),
				MatchRoomInviteState(inviteStrippedState.InviteState.Events),
			}, []roomMatcher{
				MatchRoomID(allRoomsAlicePerspective[indexBobJoined].roomID),
			}),
		),
	))

	// now bob accepts the invite
	bobJoinEvent := testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}, testutils.WithTimestamp(latestTimestamp.Add(2*time.Second)))
	allRoomsAlicePerspective[indexBobInvited].events = append(allRoomsAlicePerspective[indexBobInvited].events, bobJoinEvent)
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRoomsAlicePerspective[indexBobInvited]),
		},
	})
	v2.waitUntilEmpty(t, bob)

	// the room should be UPDATEd with the initial flag set to replace what was in the invite state
	res = v3.mustDoV3RequestWithPos(t, bobToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
			},
		},
	})
	MatchResponse(t, res, MatchV3Count(2), MatchV3Ops(
		MatchV3UpdateOp(
			0, 0, allRoomsAlicePerspective[indexBobInvited].roomID, MatchRoomRequiredState([]json.RawMessage{
				allRoomsAlicePerspective[indexBobInvited].events[0], // create event
			}),
			MatchRoomTimelineMostRecent(1, []json.RawMessage{
				bobJoinEvent,
			}),
			MatchRoomInitial(true),
			MatchRoomHighlightCount(0),
		),
	))

}
