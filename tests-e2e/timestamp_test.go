package syncv3_test

import (
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

func TestTimestamp(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	charlie := registerNewUser(t)

	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})

	var gotTs, expectedTs uint64

	lists := map[string]sync3.RequestList{
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
	}

	// Init sync to get the latest timestamp
	resAlice := alice.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 10,
			},
		},
	})
	m.MatchResponse(t, resAlice, m.MatchRoomSubscription(roomID, m.MatchRoomInitial(true)))
	timestampBeforeBobJoined := resAlice.Rooms[roomID].Timestamp

	bob.JoinRoom(t, roomID, nil)
	resAlice = alice.SlidingSyncUntilMembership(t, resAlice.Pos, roomID, bob, "join")
	resBob := bob.SlidingSync(t, sync3.Request{
		Lists: lists,
	})

	// Bob should see a different timestamp than alice, as he just joined
	gotTs = resBob.Rooms[roomID].Timestamp
	expectedTs = resAlice.Rooms[roomID].Timestamp
	if gotTs != expectedTs {
		t.Fatalf("expected timestamp to be equal, but got: %v vs %v", gotTs, expectedTs)
	}
	// ... the timestamp should still differ from what Alice received before the join
	if gotTs == timestampBeforeBobJoined {
		t.Fatalf("expected timestamp to differ, but got: %v vs %v", gotTs, timestampBeforeBobJoined)
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
	time.Sleep(time.Millisecond)

	resBob = bob.SlidingSyncUntilEventID(t, resBob.Pos, roomID, eventID)
	gotTs = resBob.Rooms[roomID].Timestamp
	expectedTs = resAlice.Rooms[roomID].Timestamp
	if gotTs != expectedTs {
		t.Fatalf("expected timestamps to be the same, but they aren't: %v vs %v", gotTs, expectedTs)
	}
	expectedTs = gotTs

	// Now send a message which bumps the timestamp in myFirstList
	eventID = alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello, world!",
		},
	})
	time.Sleep(time.Millisecond)

	resBob = bob.SlidingSyncUntilEventID(t, resBob.Pos, roomID, eventID)
	gotTs = resBob.Rooms[roomID].Timestamp
	if expectedTs == gotTs {
		t.Fatalf("expected timestamps to be different, but they aren't: %v vs %v", gotTs, expectedTs)
	}
	expectedTs = gotTs

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
	time.Sleep(time.Millisecond)

	resBob = bob.SlidingSyncUntilEventID(t, resBob.Pos, roomID, eventID)
	bobTimestampReaction := resBob.Rooms[roomID].Timestamp
	if bobTimestampReaction == expectedTs {
		t.Fatalf("expected timestamps to be different, but they aren't: %v vs %v", expectedTs, bobTimestampReaction)
	}
	expectedTs = bobTimestampReaction

	// Send another event which should NOT bump Bobs timestamp
	eventID = alice.SendEventSynced(t, roomID, Event{
		Type:     "m.room.name",
		StateKey: &emptyStateKey,
		Content: map[string]interface{}{
			"name": "random name",
		},
	})
	time.Sleep(time.Millisecond)

	resBob = bob.SlidingSyncUntilEventID(t, resBob.Pos, roomID, eventID)
	gotTs = resBob.Rooms[roomID].Timestamp
	if gotTs != expectedTs {
		t.Fatalf("expected timestamps to be the same, but they aren't: %v, expected %v", gotTs, expectedTs)
	}

	// Bob makes an initial sync again, he should still see the m.reaction timestamp
	resBob = bob.SlidingSync(t, sync3.Request{
		Lists: lists,
	})

	gotTs = resBob.Rooms[roomID].Timestamp
	expectedTs = bobTimestampReaction
	if gotTs != expectedTs {
		t.Fatalf("initial sync contains wrong timestamp: %d, expected %d", gotTs, expectedTs)
	}

	// Charlie joins the room
	charlie.JoinRoom(t, roomID, nil)
	resAlice = alice.SlidingSyncUntilMembership(t, resAlice.Pos, roomID, charlie, "join")

	resCharlie := charlie.SlidingSync(t, sync3.Request{
		Lists: lists,
	})

	// Charlie just joined so should see the same timestamp as Alice, even if
	// Charlie has the same bumpEvents as Bob, we don't leak those timestamps.
	gotTs = resCharlie.Rooms[roomID].Timestamp
	expectedTs = resAlice.Rooms[roomID].Timestamp
	if gotTs != expectedTs {
		t.Fatalf("Charlie should see the timestamp they joined, but didn't: %d, expected %d", gotTs, expectedTs)
	}
}
