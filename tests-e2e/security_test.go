package syncv3_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/complement/client"
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
	roomID := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	eve.JoinRoom(t, roomID, nil)

	// start sync streams for Alice and Eve
	aliceRes := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10}, // doesn't matter
				},
				RoomSubscription: sync3.RoomSubscription{
					RequiredState: [][2]string{
						{"m.room.name", ""},
					},
					TimelineLimit: 2,
				},
			}},
	})
	m.MatchResponse(t, aliceRes, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{roomID}),
	)))
	eveRes := eve.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10}, // doesn't matter
				},
			}},
	})
	m.MatchResponse(t, eveRes, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{roomID}),
	)))

	// kick Eve
	alice.MustDo(t, "POST", []string{"_matrix", "client", "v3", "rooms", roomID, "kick"}, client.WithJSONBody(t, map[string]interface{}{
		"user_id": eve.UserID,
	}))

	// send message as Alice, note it shouldn't go down Eve's v2 stream
	sensitiveEventID := alice.SendEventSynced(t, roomID, b.Event{
		Type:     "m.room.name",
		StateKey: ptr(""),
		Content: map[string]interface{}{
			"name": "I hate Eve",
		},
	})

	// Ensure Alice sees both events, wait till she gets them
	var timeline []json.RawMessage
	aliceRes = alice.SlidingSyncUntil(t, aliceRes.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10}, // doesn't matter
				},
				RoomSubscription: sync3.RoomSubscription{
					RequiredState: [][2]string{
						{"m.room.name", ""},
					},
					TimelineLimit: 2,
				},
			}},
	}, func(r *sync3.Response) error {
		// keep syncing until we see 2 events in the timeline
		timeline = append(timeline, r.Rooms[roomID].Timeline...)
		if len(timeline) != 2 {
			return fmt.Errorf("waiting for more messages, got %v", len(timeline))
		}
		return nil
	})

	// check Alice sees both events
	kickEvent := Event{
		Type:     "m.room.member",
		StateKey: ptr(eve.UserID),
		Content: map[string]interface{}{
			"membership": "leave",
		},
		Sender: alice.UserID,
	}
	assertEventsEqual(t, []Event{
		kickEvent,
		{
			Type:     "m.room.name",
			StateKey: ptr(""),
			Content: map[string]interface{}{
				"name": "I hate Eve",
			},
			Sender: alice.UserID,
			ID:     sensitiveEventID,
		},
	}, timeline)

	// Ensure Eve doesn't see this message in the timeline, name calc or required_state
	eveRes = eve.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10}, // doesn't matter
				},
				// Note we are _adding_ this to the list which will kick in logic to return required state / timeline limits
				// so we need to make sure that this returns no data.
				RoomSubscription: sync3.RoomSubscription{
					RequiredState: [][2]string{
						{"m.room.name", ""},
					},
					TimelineLimit: 2,
				},
			}},
	}, WithPos(eveRes.Pos))
	// the room is deleted from eve's point of view and she sees up to and including her kick event
	m.MatchResponse(t, eveRes, m.MatchList("a", m.MatchV3Count(0), m.MatchV3Ops(m.MatchV3DeleteOp(0))), m.MatchRoomSubscription(
		roomID, m.MatchRoomName(""), m.MatchRoomRequiredState(nil), MatchRoomTimelineMostRecent(1, []Event{kickEvent}),
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
	alicePrivateRoomID := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "private_chat",
	})

	// Eve is in an unrelated room
	eveUnrelatedRoomID := eve.MustCreateRoom(t, map[string]interface{}{
		"preset": "private_chat",
	})

	// seed the proxy with alice's data
	alice.SlidingSync(t, sync3.Request{})

	// start sync streams for Eve, with a room subscription to alice's private room
	eveRes := eve.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
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
	m.MatchResponse(t, eveRes, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{eveUnrelatedRoomID}),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		eveUnrelatedRoomID: {},
	}))

	// Assert that live updates still don't feed through to Eve
	alice.SendEventSynced(t, alicePrivateRoomID, b.Event{
		Type:     "m.room.name",
		StateKey: ptr(""),
		Content: map[string]interface{}{
			"name": "I hate Eve",
		},
	})
	eveRes = eve.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10}, // doesn't matter
				},
			}},
	}, WithPos(eveRes.Pos))

	// Assert that Eve doesn't see anything
	m.MatchResponse(t, eveRes, m.MatchList("a", m.MatchV3Count(1)), m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{}))
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

	roomA := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"creation_content": map[string]string{
			"type": "m.space",
		},
	})
	roomB := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "private_chat",
	})
	alice.SendEventSynced(t, roomA, b.Event{
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
		Lists: map[string]sync3.RequestList{
			"a": {
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

	roomA := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"creation_content": map[string]string{
			"type": "m.space",
		},
	})
	roomB := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	// Alice has a space A -> B
	alice.SendEventSynced(t, roomA, b.Event{
		Type:     "m.space.child",
		StateKey: &roomB,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})
	// seed the proxy with alice's data
	alice.SlidingSync(t, sync3.Request{})

	// now Eve also has a space... C -> B
	roomC := eve.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"creation_content": map[string]string{
			"type": "m.space",
		},
	})
	eve.JoinRoom(t, roomB, nil)
	eve.SendEventSynced(t, roomC, b.Event{
		Type:     "m.space.child",
		StateKey: &roomB,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})

	// ensure eve sees nothing
	res := eve.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: [][2]int64{{0, 20}},
				Filters: &sync3.RequestFilters{
					Spaces: []string{roomA},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(nil))
}

// Test that adding a child room to a space does not leak global room metadata about that
// child room to users in the parent space. This information isn't strictly confidential as
// the /rooms/{roomId}/hierarchy endpoint will include such metadata (room name, avatar, join count, etc)
// because the user is part of the parent space. There isn't an attack vector here, but repro steps:
//   - Alice and Bob are in a parent space.
//   - Bob has a poller on SS running.
//   - Alice is live streaming from SS.
//   - Bob creates a child room in that space, and sends both the m.space.parent in the child room AND
//     the m.space.child in the parent space.
//   - Ensure that no information about the child room comes down Alice's connection.
func TestSecuritySpaceChildMetadataLeakFromParent(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	parentName := "The Parent Room Name"
	childName := "The Child Room Name"

	// Alice and Bob are in a parent space.
	parentSpace := bob.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"name":   parentName,
		"creation_content": map[string]string{
			"type": "m.space",
		},
	})
	alice.MustJoinRoom(t, parentSpace, []string{"hs1"})

	// Bob has a poller on SS running.
	bobRes := bob.SlidingSync(t, sync3.Request{})

	// Alice is live streaming from SS.
	aliceRes := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: [][2]int64{{0, 20}},
			},
		},
	})
	m.MatchResponse(t, aliceRes, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		parentSpace: {
			m.MatchJoinCount(2),
			m.MatchRoomName(parentName),
		},
	}))

	// Bob creates a child room in that space, and sends both the m.space.parent in the child room AND
	// the m.space.child in the parent space.
	childRoom := bob.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"name":   childName,
		"initial_state": []map[string]interface{}{
			{
				"type":      "m.space.parent",
				"state_key": parentSpace,
				"content": map[string]interface{}{
					"canonical": true,
					"via":       []string{"hs1"},
				},
			},
		},
	})
	chlidEventID := bob.SendEventSynced(t, parentSpace, b.Event{
		Type:     "m.space.child",
		StateKey: ptr(childRoom),
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})
	// wait for SS to process it
	bob.SlidingSyncUntilEventID(t, bobRes.Pos, parentSpace, chlidEventID)

	// Ensure that no information about the child room comes down Alice's connection.
	aliceRes = alice.SlidingSync(t, sync3.Request{}, WithPos(aliceRes.Pos))
	m.MatchResponse(t, aliceRes, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		parentSpace: {
			MatchRoomTimeline([]Event{{
				Type:     "m.space.child",
				StateKey: ptr(childRoom),
				Content: map[string]interface{}{
					"via": []interface{}{"hs1"},
				},
			}}),
		},
	}))
}
