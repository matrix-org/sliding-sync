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
	var latestPos int64
	var err error
	for roomID, eventMap := range roomIDToEventMap {
		_, latestPos, err = store.Accumulate(roomID, eventMap)
		if err != nil {
			t.Fatalf("Accumulate on %s failed: %s", roomID, err)
		}
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

	// also test CurrentStateEventsInAllRooms
	roomIDToCreateEvents, err := store.CurrentStateEventsInAllRooms([]string{"m.room.create"})
	if err != nil {
		t.Fatalf("CurrentStateEventsInAllRooms returned error: %s", err)
	}
	for roomID := range roomIDToEventMap {
		if _, ok := roomIDToCreateEvents[roomID]; !ok {
			t.Fatalf("CurrentStateEventsInAllRooms missed room ID %s", roomID)
		}
	}
	for roomID := range roomIDToEventMap {
		createEvents := roomIDToCreateEvents[roomID]
		if createEvents == nil {
			t.Errorf("CurrentStateEventsInAllRooms: unknown room %v", roomID)
		}
		if len(createEvents) != 1 {
			t.Fatalf("CurrentStateEventsInAllRooms got %d events, want 1", len(createEvents))
		}
		if len(createEvents[0].JSON) < 20 { // make sure there's something here
			t.Errorf("CurrentStateEventsInAllRooms: got wrong json for event, got %s", string(createEvents[0].JSON))
		}
	}
}

// Test the examples on VisibleEventNIDsBetween docs
func TestVisibleEventNIDsBetween(t *testing.T) {
	store := NewStorage(postgresConnectionString)
	roomA := "!a:localhost"
	roomB := "!b:localhost"
	roomC := "!c:localhost"
	roomD := "!d:localhost"
	roomE := "!e:localhost"
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
		roomD: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
		},
		roomE: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
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
		numNew, _, err := store.Accumulate(tl.RoomID, tl.Events)
		if err != nil {
			t.Fatalf("Accumulate on %s failed: %s", tl.RoomID, err)
		}
		t.Logf("%s added %d new events", tl.RoomID, numNew)
	}
	latestPos, err := store.LatestEventNID()
	if err != nil {
		t.Fatalf("LatestEventNID: %s", err)
	}
	t.Logf("ABC Start=%d Latest=%d", startPos, latestPos)
	roomIDToVisibleRanges, err := store.VisibleEventNIDsBetween(alice, startPos, latestPos)
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

	// change the users else we will still have some rooms from A,B,C present if the user is still joined
	// to those rooms.
	alice = "@aliceDE:localhost"
	bob = "@bobDE:localhost"

	//                     Stream Positions
	//           1     2   3    4   5   6   7   8   9   10  11  12  13  14  15
	//   Room D  Maj                E   Mal E   Maj E   Mal E
	//   Room E        E   Mai  E                               E   Maj E   E
	timelineInjections = []struct {
		RoomID string
		Events []json.RawMessage
	}{
		{
			RoomID: roomD,
			Events: []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
			},
		},
		{
			RoomID: roomE,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
				testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{"membership": "invite"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
			},
		},
		{
			RoomID: roomD,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "leave"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "leave"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
			},
		},
		{
			RoomID: roomE,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}),
			},
		},
	}
	startPos, err = store.LatestEventNID()
	if err != nil {
		t.Fatalf("LatestEventNID: %s", err)
	}
	for _, tl := range timelineInjections {
		numNew, _, err := store.Accumulate(tl.RoomID, tl.Events)
		if err != nil {
			t.Fatalf("Accumulate on %s failed: %s", tl.RoomID, err)
		}
		t.Logf("%s added %d new events", tl.RoomID, numNew)
	}
	latestPos, err = store.LatestEventNID()
	if err != nil {
		t.Fatalf("LatestEventNID: %s", err)
	}
	t.Logf("DE Start=%d Latest=%d", startPos, latestPos)
	roomIDToVisibleRanges, err = store.VisibleEventNIDsBetween(alice, startPos, latestPos)
	if err != nil {
		t.Fatalf("VisibleEventNIDsBetween to %d: %s", latestPos, err)
	}
	for roomID, ranges := range roomIDToVisibleRanges {
		for _, r := range ranges {
			t.Logf("%v => [%d,%d]", roomID, r[0]-startPos, r[1]-startPos)
		}
	}
	if len(roomIDToVisibleRanges) != 2 {
		t.Errorf("VisibleEventNIDsBetween: wrong number of rooms, want 2 got %+v", roomIDToVisibleRanges)
	}

	// For Room D: from=1, to=15 returns { RoomD: [ [1,6], [8,10] ] } (tests multi-join/leave)
	verifyRange(t, roomIDToVisibleRanges, roomD, [][2]int64{
		{1 + startPos, 6 + startPos},
		{8 + startPos, 10 + startPos},
	})

	// For Room E: from=1, to=15 returns { RoomE: [ [3,3], [13,15] ] } (tests invites)
	verifyRange(t, roomIDToVisibleRanges, roomE, [][2]int64{
		{3 + startPos, 3 + startPos},
		{13 + startPos, 15 + startPos},
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
