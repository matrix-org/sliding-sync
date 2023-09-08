package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"

	"github.com/matrix-org/gomatrixserverlib/spec"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"github.com/matrix-org/sliding-sync/testutils"
)

func TestStorageRoomStateBeforeAndAfterEventPosition(t *testing.T) {
	ctx := context.Background()
	store := NewStorage(postgresConnectionString)
	defer store.Teardown()
	roomID := "!TestStorageRoomStateAfterEventPosition:localhost"
	alice := "@alice:localhost"
	bob := "@bob:localhost"
	events := []json.RawMessage{
		testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
		testutils.NewJoinEvent(t, alice),
		testutils.NewStateEvent(t, "m.room.join_rules", "", alice, map[string]interface{}{"join_rule": "invite"}),
		testutils.NewStateEvent(t, "m.room.member", bob, alice, map[string]interface{}{"membership": "invite"}),
	}
	_, latestNIDs, err := store.Accumulate(userID, roomID, "", events)
	if err != nil {
		t.Fatalf("Accumulate returned error: %s", err)
	}
	latest := latestNIDs[len(latestNIDs)-1]

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
	// Clean DB. If we don't, other tests' events will be in the DB, but we won't
	// provide keys in the metadata dict we pass to MetadataForAllRooms, leading to a
	// panic.
	if err := cleanDB(t); err != nil {
		t.Fatalf("failed to wipe DB: %s", err)
	}
	store := NewStorage(postgresConnectionString)
	defer store.Teardown()
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
			testutils.NewJoinEvent(t, alice),
		},
		invitedRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewJoinEvent(t, bob),
			testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{"membership": "invite"}),
		},
		leftRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
			testutils.NewJoinEvent(t, alice),
			testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "leave"}),
		},
		banRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewJoinEvent(t, bob),
			testutils.NewJoinEvent(t, alice),
			testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{"membership": "ban"}),
		},
		bobJoinedRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewJoinEvent(t, bob),
			testutils.NewJoinEvent(t, charlie),
		},
	}
	var latestPos int64
	var latestNIDs []int64
	var err error
	for roomID, eventMap := range roomIDToEventMap {
		_, latestNIDs, err = store.Accumulate(userID, roomID, "", eventMap)
		if err != nil {
			t.Fatalf("Accumulate on %s failed: %s", roomID, err)
		}
		latestPos = latestNIDs[len(latestNIDs)-1]
	}
	aliceJoinTimingsByRoomID, err := store.JoinedRoomsAfterPosition(alice, latestPos)
	if err != nil {
		t.Fatalf("failed to JoinedRoomsAfterPosition: %s", err)
	}
	if len(aliceJoinTimingsByRoomID) != 1 {
		t.Fatalf("JoinedRoomsAfterPosition at %v for %s got %v, want room %s only", latestPos, alice, aliceJoinTimingsByRoomID, joinedRoomID)
	}
	for gotRoomID, _ := range aliceJoinTimingsByRoomID {
		if gotRoomID != joinedRoomID {
			t.Fatalf("JoinedRoomsAfterPosition at %v for %s got %v want %v", latestPos, alice, gotRoomID, joinedRoomID)
		}
	}
	bobJoinTimingsByRoomID, err := store.JoinedRoomsAfterPosition(bob, latestPos)
	if err != nil {
		t.Fatalf("failed to JoinedRoomsAfterPosition: %s", err)
	}
	if len(bobJoinTimingsByRoomID) != 3 {
		t.Fatalf("JoinedRoomsAfterPosition for %s got %v rooms want %v", bob, len(bobJoinTimingsByRoomID), 3)
	}

	// also test currentNotMembershipStateEventsInAllRooms
	txn := store.DB.MustBeginTx(context.Background(), nil)
	roomIDToCreateEvents, err := store.currentNotMembershipStateEventsInAllRooms(txn, []string{"m.room.create"})
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

	newMetadata := func(roomID string, joinCount, inviteCount int) internal.RoomMetadata {
		m := internal.NewRoomMetadata(roomID)
		m.JoinCount = joinCount
		m.InviteCount = inviteCount
		return *m
	}

	// also test MetadataForAllRooms
	roomIDToMetadata := map[string]internal.RoomMetadata{
		joinedRoomID:    newMetadata(joinedRoomID, 1, 0),
		invitedRoomID:   newMetadata(invitedRoomID, 1, 1),
		banRoomID:       newMetadata(banRoomID, 1, 0),
		bobJoinedRoomID: newMetadata(bobJoinedRoomID, 2, 0),
	}

	tempTableName, err := store.PrepareSnapshot(txn)
	if err != nil {
		t.Fatalf("PrepareSnapshot: %s", err)
	}
	err = store.MetadataForAllRooms(txn, tempTableName, roomIDToMetadata)
	txn.Commit()
	if err != nil {
		t.Fatalf("MetadataForAllRooms: %s", err)
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
	defer store.Teardown()
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
			testutils.NewJoinEvent(t, bob),
		},
		roomB: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewJoinEvent(t, bob),
		},
		roomC: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewJoinEvent(t, bob),
			testutils.NewJoinEvent(t, alice),
		},
		roomD: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewJoinEvent(t, bob),
		},
		roomE: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewJoinEvent(t, bob),
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

	baseTimestamp := spec.Timestamp(1632131678061).Time()
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
				testutils.NewJoinEvent(t, alice),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
			},
		},
		{
			RoomID: roomB,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewJoinEvent(t, alice),
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
		numNew, _, err := store.Accumulate(userID, tl.RoomID, "", tl.Events)
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
				testutils.NewJoinEvent(t, alice),
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
				testutils.NewJoinEvent(t, alice),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp.Add(1*time.Second))),
				testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "leave"}),
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp.Add(1*time.Second))),
			},
		},
		{
			RoomID: roomE,
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{}, testutils.WithTimestamp(baseTimestamp)),
				testutils.NewJoinEvent(t, alice),
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
		numNew, _, err := store.Accumulate(userID, tl.RoomID, "", tl.Events)
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
	defer store.Teardown()
	roomID := "!joined:bar"
	alice := "@alice_TestStorageLatestEventsInRoomsPrevBatch:localhost"
	stateEvents := []json.RawMessage{
		testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
		testutils.NewJoinEvent(t, alice),
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
		_, _, err = store.Accumulate(userID, roomID, timeline.prevBatch, timeline.timeline)
		if err != nil {
			t.Fatalf("failed to accumulate: %s", err)
		}
		for _, ev := range timeline.timeline {
			eventIDs = append(eventIDs, gjson.ParseBytes(ev).Get("event_id").Str)
		}
	}
	t.Logf("events: %v", eventIDs)
	var idsToNIDs map[string]int64
	sqlutil.WithTransaction(store.EventsTable.db, func(txn *sqlx.Tx) error {
		idsToNIDs, err = store.EventsTable.SelectNIDsByIDs(txn, eventIDs)
		if err != nil {
			t.Fatalf("failed to get nids for events: %s", err)
		}
		return nil
	})
	t.Logf("nids: %v", idsToNIDs)
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
		eventNID := idsToNIDs[eventIDs[i]]
		// closest batch to the last event in the chunk (latest nid) is always the next prev batch token
		var pb string
		_ = sqlutil.WithTransaction(store.DB, func(txn *sqlx.Tx) (err error) {
			pb, err = store.EventsTable.SelectClosestPrevBatch(txn, roomID, eventNID)
			if err != nil {
				t.Fatalf("failed to SelectClosestPrevBatch: %s", err)
			}
			return nil
		})

		if pb != wantPrevBatch {
			t.Fatalf("SelectClosestPrevBatch: got %v want %v", pb, wantPrevBatch)
		}
	}
}

func TestGlobalSnapshot(t *testing.T) {
	alice := "@TestGlobalSnapshot_alice:localhost"
	bob := "@TestGlobalSnapshot_bob:localhost"
	roomAlice := "!alice"
	roomBob := "!bob"
	roomAliceBob := "!alicebob"
	roomSpace := "!space"
	oldRoomID := "!old"
	newRoomID := "!new"
	roomType := "room_type_here"
	spaceRoomType := "m.space"
	roomIDToEventMap := map[string][]json.RawMessage{
		roomAlice: {
			testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice, "predecessor": map[string]string{
				"room_id":  oldRoomID,
				"event_id": "$something",
			}}),
			testutils.NewJoinEvent(t, alice),
			testutils.NewStateEvent(t, "m.room.encryption", "", alice, map[string]interface{}{"algorithm": "m.megolm.v1.aes-sha2"}),
		},
		roomBob: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob, "type": roomType}),
			testutils.NewJoinEvent(t, bob),
			testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "My Room"}),
		},
		roomAliceBob: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewJoinEvent(t, bob),
			testutils.NewJoinEvent(t, alice),
			testutils.NewStateEvent(t, "m.room.canonical_alias", "", alice, map[string]interface{}{"alias": "#alias"}),
			testutils.NewStateEvent(t, "m.room.tombstone", "", alice, map[string]interface{}{"replacement_room": newRoomID, "body": "yep"}),
		},
		roomSpace: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob, "type": spaceRoomType}),
			testutils.NewJoinEvent(t, bob),
			testutils.NewStateEvent(t, "m.space.child", newRoomID, bob, map[string]interface{}{"via": []string{"somewhere"}}),
			testutils.NewStateEvent(t, "m.space.child", "!no_via", bob, map[string]interface{}{}),
			testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{"membership": "invite"}),
		},
	}
	if err := cleanDB(t); err != nil {
		t.Fatalf("failed to wipe DB: %s", err)
	}

	store := NewStorage(postgresConnectionString)
	defer store.Teardown()
	for roomID, stateEvents := range roomIDToEventMap {
		_, err := store.Initialise(roomID, stateEvents)
		assertNoError(t, err)
	}
	snapshot, err := store.GlobalSnapshot()
	assertNoError(t, err)
	wantJoinedMembers := map[string][]string{
		roomAlice:    {alice},
		roomBob:      {bob},
		roomAliceBob: {bob, alice}, // user IDs are ordered by event nid, and bob joined first so he is first
		roomSpace:    {bob},
	}
	if !reflect.DeepEqual(snapshot.AllJoinedMembers, wantJoinedMembers) {
		t.Errorf("Snapshot.AllJoinedMembers:\ngot:  %+v\nwant: %+v", snapshot.AllJoinedMembers, wantJoinedMembers)
	}
	wantMetadata := map[string]internal.RoomMetadata{
		roomAlice: {
			RoomID:               roomAlice,
			JoinCount:            1,
			LastMessageTimestamp: gjson.ParseBytes(roomIDToEventMap[roomAlice][len(roomIDToEventMap[roomAlice])-1]).Get("origin_server_ts").Uint(),
			Heroes:               []internal.Hero{{ID: alice}},
			Encrypted:            true,
			PredecessorRoomID:    &oldRoomID,
			ChildSpaceRooms:      make(map[string]struct{}),
		},
		roomBob: {
			RoomID:               roomBob,
			JoinCount:            1,
			LastMessageTimestamp: gjson.ParseBytes(roomIDToEventMap[roomBob][len(roomIDToEventMap[roomBob])-1]).Get("origin_server_ts").Uint(),
			Heroes:               []internal.Hero{{ID: bob}},
			NameEvent:            "My Room",
			RoomType:             &roomType,
			ChildSpaceRooms:      make(map[string]struct{}),
		},
		roomAliceBob: {
			RoomID:               roomAliceBob,
			JoinCount:            2,
			LastMessageTimestamp: gjson.ParseBytes(roomIDToEventMap[roomAliceBob][len(roomIDToEventMap[roomAliceBob])-1]).Get("origin_server_ts").Uint(),
			Heroes:               []internal.Hero{{ID: bob}, {ID: alice}},
			CanonicalAlias:       "#alias",
			UpgradedRoomID:       &newRoomID,
			ChildSpaceRooms:      make(map[string]struct{}),
		},
		roomSpace: {
			RoomID:               roomSpace,
			JoinCount:            1,
			InviteCount:          1,
			LastMessageTimestamp: gjson.ParseBytes(roomIDToEventMap[roomSpace][len(roomIDToEventMap[roomSpace])-1]).Get("origin_server_ts").Uint(),
			Heroes:               []internal.Hero{{ID: bob}, {ID: alice}},
			RoomType:             &spaceRoomType,
			ChildSpaceRooms: map[string]struct{}{
				newRoomID: {},
			},
		},
	}
	for roomID, want := range wantMetadata {
		assertRoomMetadata(t, snapshot.GlobalMetadata[roomID], want)
	}
}

func TestAllJoinedMembers(t *testing.T) {
	assertNoError(t, cleanDB(t))
	store := NewStorage(postgresConnectionString)
	defer store.Teardown()

	alice := "@alice:localhost"
	bob := "@bob:localhost"
	charlie := "@charlie:localhost"
	doris := "@doris:localhost"
	eve := "@eve:localhost"
	frank := "@frank:localhost"

	// Alice is always the creator and the inviter for simplicity's sake
	testCases := []struct {
		Name                  string
		InitMemberships       [][2]string
		AccumulateMemberships [][2]string
		RoomID                string // tests set this dynamically
		WantJoined            []string
		WantInvited           []string
	}{
		{
			Name:                  "basic joined users",
			InitMemberships:       [][2]string{{alice, "join"}},
			AccumulateMemberships: [][2]string{{bob, "join"}},
			WantJoined:            []string{alice, bob},
		},
		{
			Name:                  "basic invited users",
			InitMemberships:       [][2]string{{alice, "join"}, {charlie, "invite"}},
			AccumulateMemberships: [][2]string{{bob, "invite"}},
			WantJoined:            []string{alice},
			WantInvited:           []string{bob, charlie},
		},
		{
			Name:                  "many join/leaves, use latest",
			InitMemberships:       [][2]string{{alice, "join"}, {charlie, "join"}, {frank, "join"}},
			AccumulateMemberships: [][2]string{{bob, "join"}, {charlie, "leave"}, {frank, "leave"}, {charlie, "join"}, {eve, "join"}},
			WantJoined:            []string{alice, bob, charlie, eve},
		},
		{
			Name:                  "many invites, use latest",
			InitMemberships:       [][2]string{{alice, "join"}, {doris, "join"}},
			AccumulateMemberships: [][2]string{{doris, "leave"}, {charlie, "invite"}, {doris, "invite"}},
			WantJoined:            []string{alice},
			WantInvited:           []string{charlie, doris},
		},
		{
			Name:                  "invite and rejection in accumulate",
			InitMemberships:       [][2]string{{alice, "join"}},
			AccumulateMemberships: [][2]string{{frank, "invite"}, {frank, "leave"}},
			WantJoined:            []string{alice},
		},
		{
			Name:                  "invite in initial, rejection in accumulate",
			InitMemberships:       [][2]string{{alice, "join"}, {frank, "invite"}},
			AccumulateMemberships: [][2]string{{frank, "leave"}},
			WantJoined:            []string{alice},
		},
	}

	serialise := func(memberships [][2]string) []json.RawMessage {
		var result []json.RawMessage
		for _, userWithMembership := range memberships {
			target := userWithMembership[0]
			sender := userWithMembership[0]
			membership := userWithMembership[1]
			if membership == "invite" {
				// Alice is always the inviter
				sender = alice
			}
			result = append(result, testutils.NewStateEvent(t, "m.room.member", target, sender, map[string]interface{}{
				"membership": membership,
			}))
		}
		return result
	}

	for i, tc := range testCases {
		roomID := fmt.Sprintf("!TestAllJoinedMembers_%d:localhost", i)
		_, err := store.Initialise(roomID, append([]json.RawMessage{
			testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{
				"creator": alice, // alice is always the creator
			}),
		}, serialise(tc.InitMemberships)...))
		assertNoError(t, err)

		_, _, err = store.Accumulate(userID, roomID, "foo", serialise(tc.AccumulateMemberships))
		assertNoError(t, err)
		testCases[i].RoomID = roomID // remember this for later
	}

	// should get all joined members correctly
	var joinedMembers map[string][]string
	// should set join/invite counts correctly
	var roomMetadatas map[string]internal.RoomMetadata
	err := sqlutil.WithTransaction(store.DB, func(txn *sqlx.Tx) error {
		tableName, err := store.PrepareSnapshot(txn)
		if err != nil {
			return err
		}
		joinedMembers, roomMetadatas, err = store.AllJoinedMembers(txn, tableName)
		return err
	})
	assertNoError(t, err)

	for _, tc := range testCases {
		roomID := tc.RoomID
		if roomID == "" {
			t.Fatalf("test case has no room id set: %+v", tc)
		}
		// make sure joined members match
		sort.Strings(joinedMembers[roomID])
		sort.Strings(tc.WantJoined)
		if !reflect.DeepEqual(joinedMembers[roomID], tc.WantJoined) {
			t.Errorf("%v: got joined members %v want %v", tc.Name, joinedMembers[roomID], tc.WantJoined)
		}
		// make sure join/invite counts match
		wantJoined := len(tc.WantJoined)
		wantInvited := len(tc.WantInvited)
		metadata, ok := roomMetadatas[roomID]
		if !ok {
			t.Fatalf("no room metadata for room %v", roomID)
		}
		if metadata.InviteCount != wantInvited {
			t.Errorf("%v: got invite count %d want %d", tc.Name, metadata.InviteCount, wantInvited)
		}
		if metadata.JoinCount != wantJoined {
			t.Errorf("%v: got join count %d want %d", tc.Name, metadata.JoinCount, wantJoined)
		}
	}
}

func TestCircularSlice(t *testing.T) {
	testCases := []struct {
		name    string
		max     int
		appends []int64
		want    []int64 // these get sorted in the test
	}{
		{
			name:    "wraparound",
			max:     5,
			appends: []int64{9, 8, 7, 6, 5, 4, 3, 2},
			want:    []int64{2, 3, 4, 5, 6},
		},
		{
			name:    "exact",
			max:     5,
			appends: []int64{9, 8, 7, 6, 5},
			want:    []int64{5, 6, 7, 8, 9},
		},
		{
			name:    "unfilled",
			max:     5,
			appends: []int64{9, 8, 7},
			want:    []int64{7, 8, 9},
		},
		{
			name:    "wraparound x2",
			max:     5,
			appends: []int64{9, 8, 7, 6, 5, 4, 3, 2, 1, 0, 10},
			want:    []int64{0, 1, 2, 3, 10},
		},
	}
	for _, tc := range testCases {
		cs := &circularSlice{
			max: tc.max,
		}
		for _, val := range tc.appends {
			cs.append(val)
		}
		sort.Slice(cs.vals, func(i, j int) bool {
			return cs.vals[i] < cs.vals[j]
		})
		if !reflect.DeepEqual(cs.vals, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, cs.vals, tc.want)
		}

	}

}

// Test to validate that LatestEventsInRooms and LatestEventsInRoomsV2 return the same results
func TestLatestEventsInRooms(t *testing.T) {
	assertNoError(t, cleanDB(t))
	store := NewStorage(postgresConnectionString)
	defer store.Teardown()

	alice := "@alice:localhost"
	bob := "@bob:localhost"
	roomAlice := "!alice"
	roomAliceBob := "!alice_bob"
	roomIDToEventMap := map[string][]json.RawMessage{
		roomAlice: {
			testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
			testutils.NewJoinEvent(t, alice),
			testutils.NewStateEvent(t, "m.room.encryption", "", alice, map[string]interface{}{"algorithm": "m.megolm.v1.aes-sha2"}),
		},
		roomAliceBob: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewJoinEvent(t, bob),
			testutils.NewJoinEvent(t, alice),
			testutils.NewStateEvent(t, "m.room.canonical_alias", "", alice, map[string]interface{}{"alias": "#alias"}),
		},
	}

	for roomID, stateEvents := range roomIDToEventMap {
		_, err := store.Initialise(roomID, stateEvents)
		assertNoError(t, err)

		var dummyEvents []Event
		for i := 0; i <= 5; i++ {
			ev := testutils.NewMessageEvent(t, alice, "hello world!")
			dbEv := Event{
				RoomID:    roomID,
				JSON:      ev,
				ID:        "$" + roomID + alice + strconv.Itoa(i),
				PrevBatch: sql.NullString{String: "prev_batch" + alice + strconv.Itoa(i), Valid: true},
			}
			// set the last prevBatch to null, so this is tested as well
			if i == 5 {
				dbEv.PrevBatch = sql.NullString{}
			}
			dummyEvents = append(dummyEvents, dbEv)
		}

		err = sqlutil.WithTransaction(store.DB, func(txn *sqlx.Tx) error {
			_, err = store.EventsTable.Insert(txn, dummyEvents, false)
			return err
		})
		assertNoError(t, err)

	}

	limit := 5
	var to int64 = 100

	events, err := store.LatestEventsInRooms(alice, []string{roomAlice, roomAliceBob}, to, limit)
	assertNoError(t, err)

	eventsV2, err := store.LatestEventsInRoomsV2(alice, []string{roomAlice, roomAliceBob}, to, limit)
	assertNoError(t, err)

	assert.Equal(t, events, eventsV2)
}

func cleanDB(t *testing.T) error {
	// make a fresh DB which is unpolluted from other tests
	db, close := connectToDB(t)
	_, err := db.Exec(`
	DROP TABLE IF EXISTS syncv3_rooms;
	DROP TABLE IF EXISTS syncv3_invites;
	DROP TABLE IF EXISTS syncv3_snapshots;
	DROP TABLE IF EXISTS syncv3_spaces;`)
	close()
	return err
}

func assertRoomMetadata(t *testing.T, got, want internal.RoomMetadata) {
	t.Helper()
	assertValue(t, "CanonicalAlias", got.CanonicalAlias, want.CanonicalAlias)
	assertValue(t, "ChildSpaceRooms", got.ChildSpaceRooms, want.ChildSpaceRooms)
	assertValue(t, "Encrypted", got.Encrypted, want.Encrypted)
	assertValue(t, "Heroes", sortHeroes(got.Heroes), sortHeroes(want.Heroes))
	assertValue(t, "InviteCount", got.InviteCount, want.InviteCount)
	assertValue(t, "JoinCount", got.JoinCount, want.JoinCount)
	assertValue(t, "LastMessageTimestamp", got.LastMessageTimestamp, want.LastMessageTimestamp)
	assertValue(t, "NameEvent", got.NameEvent, want.NameEvent)
	assertValue(t, "PredecessorRoomID", got.PredecessorRoomID, want.PredecessorRoomID)
	assertValue(t, "RoomID", got.RoomID, want.RoomID)
	assertValue(t, "RoomType", got.RoomType, want.RoomType)
	assertValue(t, "TypingEvent", got.TypingEvent, want.TypingEvent)
	assertValue(t, "UpgradedRoomID", got.UpgradedRoomID, want.UpgradedRoomID)
}

func assertValue(t *testing.T, msg string, got, want interface{}) {
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %v want %v", msg, got, want)
	}
}

func sortHeroes(heroes []internal.Hero) []internal.Hero {
	sort.Slice(heroes, func(i, j int) bool {
		return heroes[i].ID < heroes[j].ID
	})
	return heroes
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
