package syncv3_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// The purpose of these tests is to ensure that events for one user cannot leak to another user.

// Test that events do not leak to users who have left a room.
// Rationale: When a user leaves a room they should not receive events in that room anymore. However,
// the v3 server may still be receiving events in that room from other joined members. We need to
// make sure these events don't find their way to the client.
// Attack vector:
//   - Alice is using the sync server and is in room !A.
//   - Eve joins the room !A.
//   - Alice kicks Eve.
//   - Alice sends event $X in !A.
//   - Ensure Eve does not see event $X.
func TestSecurityLiveStreamEventLeftLeak(t *testing.T) {
	alice := registerNewUser(t)
	eve := registerNewUser(t)

	// Alice and Eve in the room
	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	eve.JoinRoom(t, roomID, nil)

	// start sync streams for Alice and Eve
	aliceRes := alice.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
	})
	m.MatchResponse(t, aliceRes, m.MatchList(0, m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 10, []string{roomID}),
	)))
	eveRes := eve.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
	})
	m.MatchResponse(t, eveRes, m.MatchList(0, m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 10, []string{roomID}),
	)))

	// kick Eve
	alice.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "rooms", roomID, "kick"}, WithJSONBody(t, map[string]interface{}{
		"user_id": eve.UserID,
	}))

	// send message as Alice, note it shouldn't go down Eve's v2 stream
	sensitiveEventID := alice.SendEventSynced(t, roomID, Event{
		Type:     "m.room.name",
		StateKey: ptr(""),
		Content: map[string]interface{}{
			"name": "I hate Eve",
		},
	})
	time.Sleep(200 * time.Millisecond) // wait for the proxy to process it..

	// Ensure Alice sees both events
	aliceRes = alice.SlidingSync(t, sync3.Request{
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
	}, WithPos(aliceRes.Pos))

	timeline := aliceRes.Rooms[roomID].Timeline
	lastTwoEvents := timeline[len(timeline)-2:]
	assertEventsEqual(t, []Event{
		{
			Type:     "m.room.member",
			StateKey: ptr(eve.UserID),
			Content: map[string]interface{}{
				"membership": "leave",
			},
			Sender: alice.UserID,
		},
		{
			Type:     "m.room.name",
			StateKey: ptr(""),
			Content: map[string]interface{}{
				"name": "I hate Eve",
			},
			Sender: alice.UserID,
			ID:     sensitiveEventID,
		},
	}, lastTwoEvents)
	sensitiveEvent := lastTwoEvents[1]
	kickEvent := lastTwoEvents[0]

	// TODO: WE should be returning updated values for name and required_state
	m.MatchResponse(t, aliceRes, m.MatchList(0, m.MatchV3Count(1)), m.MatchNoV3Ops(), m.MatchRoomSubscription(
		roomID, m.MatchRoomTimelineMostRecent(2, []json.RawMessage{kickEvent, sensitiveEvent}),
	))

	// Ensure Eve doesn't see this message in the timeline, name calc or required_state
	eveRes = eve.SlidingSync(t, sync3.Request{
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
	}, WithPos(eveRes.Pos))
	// the room is deleted from eve's point of view and she sees up to and including her kick event
	m.MatchResponse(t, eveRes, m.MatchList(0, m.MatchV3Count(0), m.MatchV3Ops(m.MatchV3DeleteOp(0))), m.MatchRoomSubscription(
		roomID, m.MatchRoomName(""), m.MatchRoomRequiredState(nil), m.MatchRoomTimelineMostRecent(1, []json.RawMessage{kickEvent}),
	))
}

// Test that events do not leak via direct room subscriptions.
// Rationale: Unlike sync v2, in v3 clients can subscribe to any room ID they want as a room_subscription.
// We need to make sure that the user is allowed to see events in that room before delivering those events.
// Attack vector:
//   - Alice is using the sync server and is in room !A.
//   - Eve works out the room ID !A (this isn't sensitive information).
//   - Eve starts using the sync server and makes a room_subscription for !A.
//   - Ensure that Eve does not see any events in !A.
func TestSecurityRoomSubscriptionLeak(t *testing.T) {
	alice := registerNewUser(t)
	eve := registerNewUser(t)

	// Alice in the room
	alicePrivateRoomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "private_chat",
	})

	// Eve is in an unrelated room
	eveUnrelatedRoomID := eve.CreateRoom(t, map[string]interface{}{
		"preset": "private_chat",
	})

	// seed the proxy with alice's data
	alice.SlidingSync(t, sync3.Request{})

	// start sync streams for Eve, with a room subscription to alice's private room
	eveRes := eve.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			alicePrivateRoomID: {
				TimelineLimit: 5,
				RequiredState: [][2]string{
					{"m.room.join_rules", ""},
				},
			},
		},
	})
	// Assert that Eve doesn't see anything
	m.MatchResponse(t, eveRes, m.MatchList(0, m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 10, []string{eveUnrelatedRoomID}),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		eveUnrelatedRoomID: {},
	}))

	// Assert that live updates still don't feed through to Eve
	alice.SendEventSynced(t, alicePrivateRoomID, Event{
		Type:     "m.room.name",
		StateKey: ptr(""),
		Content: map[string]interface{}{
			"name": "I hate Eve",
		},
	})
	eveRes = eve.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
	}, WithPos(eveRes.Pos))

	// Assert that Eve doesn't see anything
	m.MatchResponse(t, eveRes, m.MatchList(0, m.MatchV3Count(1)), m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{}))
}

// Test that events do not leak via direct space subscriptions.
// Rationale: Unlike sync v2, in v3 clients can subscribe to any room ID they want as a space.
// We need to make sure that the user is allowed to see events in that room before delivering those events.
// Attack vector:
//   - Alice is using the sync server and is in space !A with room !B.
//   - Eve works out the room ID !A (this isn't sensitive information).
//   - Eve starts using the sync server and makes a request for !A as the space filter.
//   - Ensure that Eve does not see any events in !A or !B.
func TestSecuritySpaceDataLeak(t *testing.T) {
	alice := registerNewUser(t)
	eve := registerNewUser(t)

	roomA := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"creation_content": map[string]string{
			"type": "m.space",
		},
	})
	roomB := alice.CreateRoom(t, map[string]interface{}{
		"preset": "private_chat",
	})
	alice.SendEventSynced(t, roomA, Event{
		Type:     "m.space.child",
		StateKey: &roomB,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})
	// seed the proxy with alice's data
	alice.SlidingSync(t, sync3.Request{})

	// ensure eve sees nothing
	res := eve.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: [][2]int64{{0, 20}},
				Filters: &sync3.RequestFilters{
					Spaces: []string{roomA},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(nil))
}

// Test that knowledge of a room being in a hidden space does not leak via direct space subscriptions.
// Rationale: Unlike sync v2, in v3 clients can subscribe to any room ID they want as a space.
// We need to make sure that if a user is in a room in multiple spaces (only 1 of them the user is joined to)
// then they cannot see the room if they apply a filter for a parent space they are not joined to.
// Attack vector:
//   - Alice is using the sync server and is in space !A with room !B.
//   - Eve is using the sync server and is in space !C with room !B as well.
//   - Eve works out the room ID !A (this isn't sensitive information).
//   - Eve starts using the sync server and makes a request for !A as the space filter.
//   - Ensure that Eve does not see anything, even though they are joined to !B and the proxy knows it.
func TestSecuritySpaceMetadataLeak(t *testing.T) {
	alice := registerNewUser(t)
	eve := registerNewUser(t)

	roomA := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"creation_content": map[string]string{
			"type": "m.space",
		},
	})
	roomB := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	// Alice has a space A -> B
	alice.SendEventSynced(t, roomA, Event{
		Type:     "m.space.child",
		StateKey: &roomB,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})
	// seed the proxy with alice's data
	alice.SlidingSync(t, sync3.Request{})

	// now Eve also has a space... C -> B
	roomC := eve.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"creation_content": map[string]string{
			"type": "m.space",
		},
	})
	eve.JoinRoom(t, roomB, nil)
	eve.SendEventSynced(t, roomC, Event{
		Type:     "m.space.child",
		StateKey: &roomB,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})

	// ensure eve sees nothing
	res := eve.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: [][2]int64{{0, 20}},
				Filters: &sync3.RequestFilters{
					Spaces: []string{roomA},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(nil))
}
