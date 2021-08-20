package state

import (
	"encoding/json"
	"testing"

	"github.com/matrix-org/sync-v3/testutils"
)

func TestStorageJoinedRoomsAfterPosition(t *testing.T) {
	store := NewStorage(postgresConnectionString)
	joinedRoomID := "!joined:bar"
	invitedRoomID := "!invited:bar"
	leftRoomID := "!left:bar"
	banRoomID := "!ban:bar"
	bobJoinedRoomID := "!bobjoined:bar"
	alice := "@aliceTestStorageJoinedRoomsAfterPosition:localhost"
	bob := "@bobTestStorageJoinedRoomsAfterPosition:localhost"
	roomIDToEventMap := map[string][]json.RawMessage{
		joinedRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
			testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
		},
		invitedRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
			testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{"membership": "invite"}),
		},
		leftRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
			testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
			testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "leave"}),
		},
		banRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
			testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
			testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{"membership": "ban"}),
		},
		bobJoinedRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
		},
	}
	for roomID, eventMap := range roomIDToEventMap {
		_, err := store.Accumulate(roomID, eventMap)
		if err != nil {
			t.Fatalf("Accumulate on %s failed: %s", roomID, err)
		}
	}
	latestPos, err := store.LatestEventNID()
	if err != nil {
		t.Fatalf("failed to get latest event nid: %s", err)
	}
	aliceJoinedRooms, err := store.JoinedRoomsAfterPosition(alice, latestPos)
	if err != nil {
		t.Fatalf("failed to JoinedRoomsAfterPosition: %s", err)
	}
	if len(aliceJoinedRooms) != 1 || aliceJoinedRooms[0] != joinedRoomID {
		t.Fatalf("JoinedRoomsAfterPosition at %v for %s got %v want %v", latestPos, alice, aliceJoinedRooms, joinedRoomID)
	}
	bobJoinedRooms, err := store.JoinedRoomsAfterPosition(bob, latestPos)
	if err != nil {
		t.Fatalf("failed to JoinedRoomsAfterPosition: %s", err)
	}
	if len(bobJoinedRooms) != 3 {
		t.Fatalf("JoinedRoomsAfterPosition for %s got %v rooms want %v", bob, len(bobJoinedRooms), 3)
	}
}

// Test the examples on VisibleEventNIDsBetween docs
func TestVisibleEventNIDsBetween(t *testing.T) {
	store := NewStorage(postgresConnectionString)
	roomA := "!a:localhost"
	roomB := "!b:localhost"
	roomC := "!c:localhost"
	alice := "@alice_TestVisibleEventNIDsBetween:localhost"
	bob := "@bob_TestVisibleEventNIDsBetween:localhost"

	// bob makes all these rooms first, alice is already joined to C
	roomIDToEventMap := map[string][]json.RawMessage{
		roomA: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
		},
		roomB: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
		},
		roomC: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
			testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
		},
	}
	for roomID, eventMap := range roomIDToEventMap {
		_, err := store.Initialise(roomID, eventMap)
		if err != nil {
			t.Fatalf("Initialise on %s failed: %s", roomID, err)
		}
	}
	startPos, err := store.LatestEventNID()
	if err != nil {
		t.Fatalf("LatestEventNID: %s", err)
	}

	// Test the examples
	//                     Stream Positions
	//           1     2   3    4   5   6   7   8   9   10
	//   Room A  Maj   E   E                E
	//   Room B                 E   Maj E
	//   Room C                                 E   Mal E   (a already joined to this room)
	timelineInjections := []struct {
		RoomID string
		Events []json.RawMessage
	}{
		{
			RoomID: roomA,
			Events: []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
			},
		},
		{
			RoomID: roomB,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
			},
		},
		{
			RoomID: roomA,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
			},
		},
		{
			RoomID: roomC,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "leave"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
			},
		},
	}
	for _, tl := range timelineInjections {
		numNew, err := store.Accumulate(tl.RoomID, tl.Events)
		if err != nil {
			t.Fatalf("Accumulate on %s failed: %s", tl.RoomID, err)
		}
		t.Logf("%s added %d new events", tl.RoomID, numNew)
	}
	latestPos, err := store.LatestEventNID()
	if err != nil {
		t.Fatalf("LatestEventNID: %s", err)
	}
	t.Logf("Start=%d Latest=%d", startPos, latestPos)
	roomIDToVisibleRanges, err := store.VisibleEventNIDsBetween(alice, 0, latestPos)
	if err != nil {
		t.Fatalf("VisibleEventNIDsBetween to %d: %s", latestPos, err)
	}
	//   - For Room A: from=1, to=10, returns { RoomA: [ [1,7] ]}  (tests events in joined room)
	//   - For Room B: from=1, to=10, returns { RoomB: [ [5,6] ]}  (tests joining a room starts events)
	//   - For Room C: from=1, to=10, returns { RoomC: [ [8,9] ]}  (tests leaving a room stops events)
	if len(roomIDToVisibleRanges) != 3 {
		t.Fatalf("VisibleEventNIDsBetween: wrong number of rooms, want 3 got %+v", roomIDToVisibleRanges)
	}
	t.Logf("Result: %+v", roomIDToVisibleRanges)
	// TODO: Assert positions

}
