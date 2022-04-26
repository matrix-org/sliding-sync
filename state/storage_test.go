package state

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sqlutil"
	"github.com/matrix-org/sync-v3/testutils"
	"github.com/tidwall/gjson"
)

func TestStorageRoomStateBeforeAndAfterEventPosition(t *testing.T) {
	ctx := context.Background()
	store := NewStorage(postgresConnectionString)
	roomID := "!TestStorageRoomStateAfterEventPosition:localhost"
	alice := "@alice:localhost"
	bob := "@bob:localhost"
	events := []json.RawMessage{
		testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
		testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
		testutils.NewStateEvent(t, "m.room.join_rules", "", alice, map[string]interface{}{"join_rule": "invite"}),
		testutils.NewStateEvent(t, "m.room.member", bob, alice, map[string]interface{}{"membership": "invite"}),
	}
	_, latest, err := store.Accumulate(roomID, "", events)
	if err != nil {
		t.Fatalf("Accumulate returned error: %s", err)
	}

	testCases := []struct {
		name       string
		getEvents  func() []Event
		wantEvents []json.RawMessage
	}{
		{
			name: "room state after the latest position includes the invite event",
			getEvents: func() []Event {
				events, err := store.RoomStateAfterEventPosition(ctx, []string{roomID}, latest, nil)
				if err != nil {
					t.Fatalf("RoomStateAfterEventPosition: %s", err)
				}
				return events[roomID]
			},
			wantEvents: events[:],
		},
		{
			name: "room state after the latest position filtered for join_rule returns a single event",
			getEvents: func() []Event {
				events, err := store.RoomStateAfterEventPosition(ctx, []string{roomID}, latest, map[string][]string{"m.room.join_rules": nil})
				if err != nil {
					t.Fatalf("RoomStateAfterEventPosition: %s", err)
				}
				return events[roomID]
			},
			wantEvents: []json.RawMessage{
				events[2],
			},
		},
		{
			name: "room state after the latest position filtered for join_rule and create event excludes member events",
			getEvents: func() []Event {
				events, err := store.RoomStateAfterEventPosition(ctx, []string{roomID}, latest, map[string][]string{
					"m.room.join_rules": []string{""},
					"m.room.create":     nil, // all matching state events with this event type
				})
				if err != nil {
					t.Fatalf("RoomStateAfterEventPosition: %s", err)
				}
				return events[roomID]
			},
			wantEvents: []json.RawMessage{
				events[0], events[2],
			},
		},
		{
			name: "room state after the latest position filtered for all members returns all member events",
			getEvents: func() []Event {
				events, err := store.RoomStateAfterEventPosition(ctx, []string{roomID}, latest, map[string][]string{
					"m.room.member": nil, // all matching state events with this event type
				})
				if err != nil {
					t.Fatalf("RoomStateAfterEventPosition: %s", err)
				}
				return events[roomID]
			},
			wantEvents: []json.RawMessage{
				events[1], events[3],
			},
		},
	}

	for _, tc := range testCases {
		gotEvents := tc.getEvents()
		if len(gotEvents) != len(tc.wantEvents) {
			t.Errorf("%s: got %d events want %d : got %+v", tc.name, len(gotEvents), len(tc.wantEvents), gotEvents)
			continue
		}
		for i, eventJSON := range tc.wantEvents {
			if !bytes.Equal(eventJSON, gotEvents[i].JSON) {
				t.Errorf("%s: pos %d\ngot  %s\nwant %s", tc.name, i, string(gotEvents[i].JSON), string(eventJSON))
			}
		}
	}
}

func TestStorageJoinedRoomsAfterPosition(t *testing.T) {
	store := NewStorage(postgresConnectionString)
	joinedRoomID := "!joined:bar"
	invitedRoomID := "!invited:bar"
	leftRoomID := "!left:bar"
	banRoomID := "!ban:bar"
	bobJoinedRoomID := "!bobjoined:bar"
	alice := "@aliceTestStorageJoinedRoomsAfterPosition:localhost"
	bob := "@bobTestStorageJoinedRoomsAfterPosition:localhost"
	charlie := "@charlieTestStorageJoinedRoomsAfterPosition:localhost"
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
			testutils.NewStateEvent(t, "m.room.member", charlie, charlie, map[string]interface{}{"membership": "join"}),
		},
	}
	var latestPos int64
	var err error
	for roomID, eventMap := range roomIDToEventMap {
		_, latestPos, err = store.Accumulate(roomID, "", eventMap)
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

	// also test currentStateEventsInAllRooms
	roomIDToCreateEvents, err := store.currentStateEventsInAllRooms([]string{"m.room.create"})
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

	// also test MetadataForAllRooms
	roomIDToMetadata, err := store.MetadataForAllRooms()
	if err != nil {
		t.Fatalf("HeroInfoForAllRooms: %s", err)
	}
	wantHeroInfos := map[string]internal.RoomMetadata{
		joinedRoomID: {
			JoinCount: 1,
		},
		invitedRoomID: {
			JoinCount:   1,
			InviteCount: 1,
		},
		banRoomID: {
			JoinCount: 1,
		},
		bobJoinedRoomID: {
			JoinCount: 2,
		},
	}
	for roomID, wantHI := range wantHeroInfos {
		gotHI := roomIDToMetadata[roomID]
		if gotHI.InviteCount != wantHI.InviteCount {
			t.Errorf("hero info for %s got %d invited users, want %d", roomID, gotHI.InviteCount, wantHI.InviteCount)
		}
		if gotHI.JoinCount != wantHI.JoinCount {
			t.Errorf("hero info for %s got %d joined users, want %d", roomID, gotHI.JoinCount, wantHI.JoinCount)
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

	baseTimestamp := gomatrixserverlib.Timestamp(1632131678061).Time()
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
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
			},
		},
		{
			RoomID: roomB,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
			},
		},
		{
			RoomID: roomA,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
			},
		},
		{
			RoomID: roomC,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "leave"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
			},
		},
	}
	for _, tl := range timelineInjections {
		numNew, _, err := store.Accumulate(tl.RoomID, "", tl.Events)
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

	// check that we can query subsets too
	roomIDToVisibleRangesSubset, err := store.visibleEventNIDsBetweenForRooms(alice, []string{roomA, roomB}, startPos, latestPos)
	if err != nil {
		t.Fatalf("VisibleEventNIDsBetweenForRooms to %d: %s", latestPos, err)
	}
	if len(roomIDToVisibleRangesSubset) != 2 {
		t.Errorf("VisibleEventNIDsBetweenForRooms: wrong number of rooms, want 2 got %+v", roomIDToVisibleRanges)
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
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{"membership": "invite"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp.Add(1*time.Second))),
			},
		},
		{
			RoomID: roomD,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "leave"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp.Add(1*time.Second))),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "leave"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp.Add(1*time.Second))),
			},
		},
		{
			RoomID: roomE,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
			},
		},
	}
	startPos, err = store.LatestEventNID()
	if err != nil {
		t.Fatalf("LatestEventNID: %s", err)
	}
	for _, tl := range timelineInjections {
		numNew, _, err := store.Accumulate(tl.RoomID, "", tl.Events)
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

func TestStorageLatestEventsInRoomsPrevBatch(t *testing.T) {
	store := NewStorage(postgresConnectionString)
	roomID := "!joined:bar"
	alice := "@alice_TestStorageLatestEventsInRoomsPrevBatch:localhost"
	stateEvents := []json.RawMessage{
		testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
		testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
	}
	timelines := []struct {
		timeline  []json.RawMessage
		prevBatch string
	}{
		{
			prevBatch: "batch A",
			timeline: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "1"}), // prev batch should be associated with this event
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "2"}),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "3"}),
			},
		},
		{
			prevBatch: "batch B",
			timeline: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "4"}),
			},
		},
		{
			prevBatch: "batch C",
			timeline: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "5"}),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "6"}),
			},
		},
	}

	_, err := store.Initialise(roomID, stateEvents)
	if err != nil {
		t.Fatalf("failed to initialise: %s", err)
	}
	eventIDs := []string{}
	for _, timeline := range timelines {
		_, _, err = store.Accumulate(roomID, timeline.prevBatch, timeline.timeline)
		if err != nil {
			t.Fatalf("failed to accumulate: %s", err)
		}
		for _, ev := range timeline.timeline {
			eventIDs = append(eventIDs, gjson.ParseBytes(ev).Get("event_id").Str)
		}
	}
	t.Logf("events: %v", eventIDs)
	var eventNIDs []int64
	sqlutil.WithTransaction(store.EventsTable.db, func(txn *sqlx.Tx) error {
		eventNIDs, err = store.EventsTable.SelectNIDsByIDs(txn, eventIDs)
		if err != nil {
			t.Fatalf("failed to get nids for events: %s", err)
		}
		return nil
	})
	t.Logf("nids: %v", eventNIDs)
	wantPrevBatches := []string{
		// first chunk
		timelines[0].prevBatch,
		timelines[1].prevBatch,
		timelines[1].prevBatch,
		// second chunk
		timelines[1].prevBatch,
		// third chunk
		timelines[2].prevBatch,
		"",
	}

	for i := range wantPrevBatches {
		wantPrevBatch := wantPrevBatches[i]
		eventNID := eventNIDs[i]
		// closest batch to the last event in the chunk (latest nid) is always the next prev batch token
		pb, err := store.EventsTable.SelectClosestPrevBatch(roomID, eventNID)
		if err != nil {
			t.Fatalf("failed to SelectClosestPrevBatch: %s", err)
		}
		if pb != wantPrevBatch {
			t.Fatalf("SelectClosestPrevBatch: got %v want %v", pb, wantPrevBatch)
		}
	}
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
