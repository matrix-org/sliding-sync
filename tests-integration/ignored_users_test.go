package syncv3

import (
	"encoding/json"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"testing"
	"time"
)

// Test that messages from ignored users are not sent to clients, even if they appear
// on someone else's poller first.
func TestIgnoredUsers(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	const nigel = "@nigel:localhost"
	roomID := "!unimportant"

	v2.addAccount(t, alice, aliceToken)
	v2.addAccount(t, bob, bobToken)

	// Bob creates a room. Nigel and Alice join.
	state := createRoomState(t, bob, time.Now())
	state = append(state, testutils.NewStateEvent(t, "m.room.member", nigel, nigel, map[string]interface{}{
		"membership": "join",
	}))
	aliceJoin := testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{
		"membership": "join",
	})

	t.Log("Alice and Bob's pollers sees Alice's join.")
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				state:  state,
				events: []json.RawMessage{aliceJoin},
			}),
		},
		NextBatch: "alice_sync_1",
	})
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				state:  state,
				events: []json.RawMessage{aliceJoin},
			}),
		},
		NextBatch: "bob_sync_1",
	})

	t.Log("Alice sliding syncs.")
	aliceRes := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 10}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 20,
				},
			},
		},
	})
	t.Log("She should see her join.")
	m.MatchResponse(t, aliceRes, m.MatchRoomSubscription(roomID, m.MatchRoomTimeline([]json.RawMessage{aliceJoin})))

	t.Log("Bob sliding syncs.")
	bobRes := v3.mustDoV3Request(t, bobToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 10}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 20,
				},
			},
		},
	})
	t.Log("He should see Alice's join.")
	m.MatchResponse(t, bobRes, m.MatchRoomSubscription(roomID, m.MatchRoomTimeline([]json.RawMessage{aliceJoin})))

	t.Log("Alice ignores Nigel.")
	v2.queueResponse(alice, sync2.SyncResponse{
		AccountData: sync2.EventsResponse{
			Events: []json.RawMessage{
				testutils.NewAccountData(t, "m.ignored_user_list", map[string]any{
					"ignored_users": map[string]any{
						nigel: map[string]any{},
					},
				}),
			},
		},
		NextBatch: "alice_sync_2",
	})
	v2.waitUntilEmpty(t, alice)

	t.Log("Bob's poller sees a message from Nigel, then a message from Alice.")
	messages := []json.RawMessage{
		testutils.NewMessageEvent(t, nigel, "naughty nigel"),
		testutils.NewMessageEvent(t, alice, "angelic alice"),
	}
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: messages,
			}),
		},
		NextBatch: "bob_sync_2",
	})
	v2.waitUntilEmpty(t, bob)

	t.Log("Bob syncs. He should see both messages.")
	bobRes = v3.mustDoV3RequestWithPos(t, bobToken, bobRes.Pos, sync3.Request{})
	m.MatchResponse(t, bobRes, m.MatchRoomSubscription(roomID, m.MatchRoomTimeline(messages)))

	t.Log("Alice syncs. She should see her message, but not Nigel's.")
	aliceRes = v3.mustDoV3RequestWithPos(t, aliceToken, aliceRes.Pos, sync3.Request{})
	m.MatchResponse(t, aliceRes, m.MatchRoomSubscription(roomID, m.MatchRoomTimeline(messages[1:])))

	t.Log("Bob's poller sees Nigel set a custom state event")
	nigelState := testutils.NewStateEvent(t, "com.example.fruit", "banana", nigel, map[string]any{})
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{nigelState},
			}),
		},
		NextBatch: "bob_sync_3",
	})
	v2.waitUntilEmpty(t, bob)

	t.Log("Alice syncs. She should see Nigel's state event.")
	aliceRes = v3.mustDoV3RequestWithPos(t, aliceToken, aliceRes.Pos, sync3.Request{})
	m.MatchResponse(t, aliceRes, m.MatchRoomSubscription(roomID, m.MatchRoomTimeline([]json.RawMessage{nigelState})))

}
