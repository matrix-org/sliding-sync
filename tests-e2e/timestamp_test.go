package syncv3_test

import (
	"testing"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

func TestTimestamp(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})

	// Init sync to get the latest timestamp
	res := alice.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 10,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscription(roomID, m.MatchRoomInitial(true)))
	timestampBeforeBobJoined := res.Rooms[roomID].Timestamp

	bob.JoinRoom(t, roomID, nil)
	res = alice.SlidingSyncUntilMembership(t, res.Pos, roomID, bob, "join")

	resBob := bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"myFirstList": {
				Ranges: [][2]int64{{0, 1}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 10,
				},
				BumpEventTypes: []string{"m.room.message"}, // only messages bump the timestamp
			},
			"mySecondList": {
				Ranges: [][2]int64{{0, 1}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 10,
				},
				BumpEventTypes: []string{"m.reaction"}, // only reactions bump the timestamp
			},
		},
	})

	// Bob should see a different timestamp than alice, as he just joined
	bobTimestampBefore := resBob.Rooms[roomID].Timestamp
	if bobTimestampBefore == timestampBeforeBobJoined {
		t.Fatalf("expected timestamp to differ: %v vs %v", timestampBeforeBobJoined, bobTimestampBefore)
	}

	// Send an event which should NOT bump Bobs timestamp, because it is not listed it
	// any BumpEventTypes
	emptyStateKey := ""
	eventID := alice.SendEventSynced(t, roomID, Event{
		Type:     "m.room.topic",
		StateKey: &emptyStateKey,
		Content: map[string]interface{}{
			"topic": "random topic",
		},
	})

	bobTimestampAfter := resBob.Rooms[roomID].Timestamp

	resBob = bob.SlidingSyncUntilEventID(t, resBob.Pos, roomID, eventID)
	t.Logf("bobTimestampBefore vs bobTimestampAfter: %v %v", bobTimestampBefore, bobTimestampAfter)
	if bobTimestampBefore != bobTimestampAfter {
		t.Fatalf("expected timestamps to be the same, but they aren't: %v vs %v", bobTimestampBefore, bobTimestampAfter)
	}
	bobTimestampBefore = bobTimestampAfter

	// Now send a message which bumps the timestamp in myFirstList
	eventID = alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello, world!",
		},
	})

	resBob = bob.SlidingSyncUntilEventID(t, resBob.Pos, roomID, eventID)
	bobTimestampAfter = resBob.Rooms[roomID].Timestamp
	if bobTimestampBefore == bobTimestampAfter {
		t.Fatalf("expected timestamps to be different, but they aren't: %v vs %v", bobTimestampBefore, bobTimestampAfter)
	}
	bobTimestampBefore = bobTimestampAfter

	// Now send a message which bumps the timestamp in mySecondList
	eventID = alice.SendEventSynced(t, roomID, Event{
		Type: "m.reaction",
		Content: map[string]interface{}{
			"m.relates.to": map[string]interface{}{
				"event_id": eventID,
				"key":      "✅",
				"rel_type": "m.annotation",
			},
		},
	})

	resBob = bob.SlidingSyncUntilEventID(t, resBob.Pos, roomID, eventID)
	bobTimestampAfter = resBob.Rooms[roomID].Timestamp
	if bobTimestampBefore == bobTimestampAfter {
		t.Fatalf("expected timestamps to be different, but they aren't: %v vs %v", bobTimestampBefore, bobTimestampAfter)
	}
	bobTimestampBefore = bobTimestampAfter

	// Send another event which should NOT bump Bobs timestamp
	eventID = alice.SendEventSynced(t, roomID, Event{
		Type:     "m.room.name",
		StateKey: &emptyStateKey,
		Content: map[string]interface{}{
			"name": "random name",
		},
	})

	resBob = bob.SlidingSyncUntilEventID(t, resBob.Pos, roomID, eventID)
	bobTimestampAfter = resBob.Rooms[roomID].Timestamp
	if bobTimestampBefore != bobTimestampAfter {
		t.Fatalf("expected timestamps to be the same, but they aren't: %v vs %v", bobTimestampBefore, bobTimestampAfter)
	}
}
