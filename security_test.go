package syncv3

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
)

// The purpose of these tests is to ensure that events for one user cannot leak to another user.

// Test that events do not leak to users who have left a room.
// Rationale: When a user leaves a room they should not receive events in that room anymore. However,
// the v3 server may still be receiving events in that room from other joined members. We need to
// make sure these events don't find their way to the client.
// Attack vector:
//  - Alice is using the sync server and is in room !A.
//  - Eve joins the room !A.
//  - Alice kicks Eve.
//  - Alice sends event $X in !A.
//  - Ensure Eve does not see event $X.
func TestSecurityLiveStreamEventLeftLeak(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!TestSecurityLiveStreamEventLeftLeak_a:localhost"
	eve := "@TestSecurityLiveStreamEventLeftLeak_eve:localhost"
	eveToken := "EVE_BEARER_TOKEN_TestSecurityLiveStreamEventLeftLeak"
	v2.addAccount(alice, aliceToken)
	v2.addAccount(eve, eveToken)
	eveJoinEvent := testutils.NewStateEvent(
		t, "m.room.member", eve, eve, map[string]interface{}{"membership": "join"},
		testutils.WithTimestamp(time.Now().Add(5*time.Second)),
	)
	// Alice and Eve in the room
	theRoom := roomEvents{
		roomID: roomID,
		events: append(createRoomState(t, alice, time.Now()), eveJoinEvent),
	}
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(theRoom),
		},
	})
	v2.queueResponse(eve, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{eveJoinEvent},
			}),
		},
	})

	// start sync streams for Alice and Eve
	tokenToPos := map[string]string{
		aliceToken: "",
		eveToken:   "",
	}
	for _, token := range []string{aliceToken, eveToken} {
		res := v3.mustDoV3Request(t, token, sync3.Request{
			Lists: []sync3.RequestList{{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10}, // doesn't matter
				},
			}},
		})
		MatchResponse(t, res, MatchV3Count(1), MatchV3Ops(MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != 1 {
				return fmt.Errorf("range missing room: got %d want 1", len(op.Rooms))
			}
			return theRoom.MatchRoom(op.Rooms[0])
		})))
		tokenToPos[token] = res.Pos
	}

	// kick Eve
	kickEvent := testutils.NewStateEvent(
		t, "m.room.member", eve, alice, map[string]interface{}{"membership": "leave"},
		testutils.WithTimestamp(time.Now().Add(6*time.Second)),
	)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{kickEvent},
			}),
		},
	})
	v2.queueResponse(eve, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{kickEvent},
			}),
		},
	})

	// send message as Alice, note it doesn't go down Eve's v2 stream
	sensitiveEvent := testutils.NewStateEvent(
		t, "m.room.name", "", alice, map[string]interface{}{"name": "I hate Eve"},
		testutils.WithTimestamp(time.Now().Add(7*time.Second)),
	)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{sensitiveEvent},
			}),
		},
	})

	// let things be processed
	v2.waitUntilEmpty(t, alice)
	v2.waitUntilEmpty(t, eve)

	// Ensure Eve doesn't see this message in the timeline, name calc or required_state
	res := v3.mustDoV3RequestWithPos(t, eveToken, tokenToPos[eveToken], sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
			RoomSubscription: sync3.RoomSubscription{
				RequiredState: [][2]string{
					{"m.room.name", ""},
				},
			},
		}},
	})
	// TODO: We include left counts mid-sync so clients can see the user has left/been kicked. Should be configurable.
	MatchResponse(t, res, MatchV3Count(1), MatchV3Ops(
		MatchV3UpdateOp(0, 0, roomID, MatchRoomName(""), MatchRoomRequiredState(nil), MatchRoomTimelineMostRecent(1, []json.RawMessage{kickEvent})),
	))

	// Ensure Alice does see both events
	res = v3.mustDoV3RequestWithPos(t, aliceToken, tokenToPos[aliceToken], sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
			RoomSubscription: sync3.RoomSubscription{
				RequiredState: [][2]string{
					{"m.room.name", ""},
				},
			},
		}},
	})
	// TODO: We should consolidate 2x UPDATEs into 1x if we get scenarios like this
	// TODO: WE should be returning updated values for name and required_state
	MatchResponse(t, res, MatchV3Count(1), MatchV3Ops(
		MatchV3UpdateOp(
			0, 0, roomID, MatchRoomTimelineMostRecent(1, []json.RawMessage{kickEvent}),
		),
		MatchV3UpdateOp(
			0, 0, roomID, MatchRoomTimelineMostRecent(1, []json.RawMessage{sensitiveEvent}),
		),
	))
}

// Test that events do not leak via direct room subscriptions.
// Rationale: Unlike sync v2, in v3 clients can subscribe to any room ID they want as a room_subscription.
// We need to make sure that the user is allowed to see events in that room before delivering those events.
// Attack vector:
//  - Alice is using the sync server and is in room !A.
//  - Eve works out the room ID !A (this isn't sensitive information).
//  - Eve starts using the sync server and makes a room_subscription for !A.
//  - Ensure that Eve does not see any events in !A.
func TestSecurityRoomSubscriptionLeak(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	secretRoomID := "!TestSecurityRoomSubscriptionLeak:localhost"
	unrelatedRoomID := "!unrelated:localhost"
	eve := "@TestSecurityRoomSubscriptionLeak_eve:localhost"
	eveToken := "EVE_BEARER_TOKEN_TestSecurityRoomSubscriptionLeak"
	v2.addAccount(alice, aliceToken)
	v2.addAccount(eve, eveToken)

	// Alice in the room
	theRoom := roomEvents{
		roomID: secretRoomID,
		events: createRoomState(t, alice, time.Now()),
	}
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(theRoom),
		},
	})
	// Eve is in an unrelated room
	v2.queueResponse(eve, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: unrelatedRoomID,
				state:  createRoomState(t, eve, time.Now()),
			}),
		},
	})
	// do initial sync so we start pollers
	_ = v3.mustDoV3Request(t, aliceToken, sync3.Request{})
	_ = v3.mustDoV3Request(t, eveToken, sync3.Request{})

	// start sync streams for Eve, with a room subscription to the secret room
	res := v3.mustDoV3Request(t, eveToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			secretRoomID: {
				TimelineLimit: 5,
				RequiredState: [][2]string{
					{"m.room.join_rules", ""},
				},
			},
		},
	})
	// Assert that Eve doesn't see anything
	MatchResponse(t, res, MatchV3Count(1), MatchV3Ops(
		MatchV3SyncOpWithMatchers(
			MatchRoomRange([]roomMatcher{
				MatchRoomID(unrelatedRoomID),
			}),
		),
	), MatchRoomSubscriptions(true, map[string][]roomMatcher{}))

	// Assert that live updates still don't feed through to Eve
	sensitiveEvent := testutils.NewStateEvent(
		t, "m.room.name", "", alice, map[string]interface{}{"name": "I hate Eve"},
		testutils.WithTimestamp(time.Now().Add(7*time.Second)),
	)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: secretRoomID,
				events: []json.RawMessage{sensitiveEvent},
			}),
		},
	})
	v2.waitUntilEmpty(t, alice)

	res = v3.mustDoV3RequestWithPos(t, eveToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			secretRoomID: {
				TimelineLimit: 5,
				RequiredState: [][2]string{
					{"m.room.join_rules", ""},
				},
			},
		},
	})
	// Assert that Eve doesn't see anything
	MatchResponse(t, res, MatchV3Count(1), MatchV3Ops(), MatchRoomSubscriptions(true, map[string][]roomMatcher{}))
}
