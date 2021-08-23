package state

import (
	"encoding/json"
	"reflect"
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
	for roomID, ranges := range roomIDToVisibleRanges {
		for _, r := range ranges {
			t.Logf("%v => [%d,%d]", roomID, r[0]-startPos, r[1]-startPos)
		}
	}
	if len(roomIDToVisibleRanges) != 3 {
		t.Errorf("VisibleEventNIDsBetween: wrong number of rooms, want 3 got %+v", roomIDToVisibleRanges)
	}

	// For Room A: from=1, to=10, returns { RoomA: [ [1,10] ]}  (tests events in joined room)
	verifyRange(t, roomIDToVisibleRanges, roomA, [][2]int64{
		{1 + startPos, 10 + startPos},
	})

	// For Room B: from=1, to=10, returns { RoomB: [ [5,10] ]}  (tests joining a room starts events)
	verifyRange(t, roomIDToVisibleRanges, roomB, [][2]int64{
		{5 + startPos, 10 + startPos},
	})

	// For Room C: from=1, to=10, returns { RoomC: [ [0,9] ]}  (tests leaving a room stops events)
	// We start at 0 because it's the earliest event (we were joined since the beginning of the room state)
	verifyRange(t, roomIDToVisibleRanges, roomC, [][2]int64{
		{0 + startPos, 9 + startPos},
	})
}

func verifyRange(t *testing.T, result map[string][][2]int64, roomID string, wantRanges [][2]int64) {
	t.Helper()
	gotRanges := result[roomID]
	if gotRanges == nil {
		t.Fatalf("no range was returned for room %s", roomID)
	}
	if len(gotRanges) != len(wantRanges) {
		t.Fatalf("%s range count mismatch, got %d ranges, want %d :: GOT=%+v WANT=%+v", roomID, len(gotRanges), len(wantRanges), gotRanges, wantRanges)
	}
	for i := range gotRanges {
		if !reflect.DeepEqual(gotRanges[i], wantRanges[i]) {
			t.Errorf("%s range at index %d got %v want %v", roomID, i, gotRanges[i], wantRanges[i])
		}
	}
}
