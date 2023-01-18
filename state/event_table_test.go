package state

import (
	"bytes"
	"database/sql"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/matrix-org/sliding-sync/sqlutil"
	"github.com/matrix-org/sliding-sync/testutils"
)

func TestEventTable(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	roomID := "!0:localhost"
	table := NewEventTable(db)
	events := []Event{
		{
			ID:   "100",
			JSON: []byte(`{"event_id":"100", "foo":"bar", "type": "T1", "state_key":"S1", "room_id":"` + roomID + `"}`),
		},
		{
			ID:   "101",
			JSON: []byte(`{"event_id":"101",  "foo":"bar", "type": "T2", "state_key":"S2", "room_id":"` + roomID + `"}`),
		},
		{
			// ID is optional, it will pull event_id out if it's missing
			JSON: []byte(`{"event_id":"102", "foo":"bar", "type": "T3", "state_key":"", "room_id":"` + roomID + `"}`),
		},
	}
	idToNID, err := table.Insert(txn, events, true)
	if err != nil {
		t.Fatalf("Insert failed: %s", err)
	}
	if len(idToNID) != len(events) {
		t.Fatalf("wanted %d new events, got %d", len(events), len(idToNID))
	}
	txn.Commit()

	txn, err = db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Rollback()
	// duplicate insert is ok
	idToNID, err = table.Insert(txn, events, true)
	if err != nil {
		t.Fatalf("Insert failed: %s", err)
	}
	if len(idToNID) != 0 {
		t.Fatalf("wanted 0 new events, got %d", len(idToNID))
	}

	// pulling non-existent ids returns no error but a zero slice
	events, err = table.SelectByIDs(txn, false, []string{"101010101010"})
	if err != nil {
		t.Fatalf("SelectByIDs failed: %s", err)
	}
	if len(events) != 0 {
		t.Fatalf("SelectByIDs returned %d events, wanted none", len(events))
	}

	// pulling events by event_id is ok
	events, err = table.SelectByIDs(txn, true, []string{"100", "101", "102"})
	if err != nil {
		t.Fatalf("SelectByIDs failed: %s", err)
	}
	if len(events) != 3 {
		t.Fatalf("SelectByIDs returned %d events, want 3", len(events))
	}

	// pulling nids by event_id is ok
	idToNIDs, err := table.SelectNIDsByIDs(txn, []string{"100", "101", "102"})
	if err != nil {
		t.Fatalf("SelectNIDsByIDs failed: %s", err)
	}
	if len(idToNIDs) != 3 {
		t.Fatalf("SelectNIDsByIDs returned %v, want 3", idToNIDs)
	}

	// set a snapshot ID on them
	var firstSnapshotID int64 = 55
	for _, nid := range idToNIDs {
		if err = table.UpdateBeforeSnapshotID(txn, nid, firstSnapshotID, 0); err != nil {
			t.Fatalf("UpdateSnapshotID: %s", err)
		}
	}
	// query the snapshot
	latestEvents, err := table.LatestEventInRooms(txn, []string{roomID}, idToNIDs["101"])
	le := latestEvents[0]
	if err != nil {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: %s", err)
	}
	if int64(le.BeforeStateSnapshotID) != firstSnapshotID {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: got snap ID %d want %d", int64(le.BeforeStateSnapshotID), firstSnapshotID)
	}
	// the queried position
	if le.NID != idToNIDs["101"] {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: didn't return last inserted event, got %d want %d", le.NID, idToNIDs["101"])
	}
	// try again with a much higher pos
	latestEvents, err = table.LatestEventInRooms(txn, []string{roomID}, 999999)
	le = latestEvents[0]
	if err != nil {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: %s", err)
	}
	if int64(le.BeforeStateSnapshotID) != firstSnapshotID {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: got snap ID %d want %d", le.BeforeStateSnapshotID, firstSnapshotID)
	}
	if le.NID != idToNIDs["102"] {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: didn't return last inserted event, got %d want %d", le.NID, idToNIDs["102"])
	}

	// find max and query it
	var wantHighestNID int64
	for _, nid := range idToNIDs {
		if nid > wantHighestNID {
			wantHighestNID = nid
		}
	}
	gotHighestNID, err := table.SelectHighestNID()
	if err != nil {
		t.Fatalf("SelectHighestNID returned error: %s", err)
	}
	if wantHighestNID != gotHighestNID {
		t.Errorf("SelectHighestNID didn't select highest, got %d want %d", gotHighestNID, wantHighestNID)
	}

	var nids []int64
	for _, ev := range events {
		nids = append(nids, int64(ev.NID))
	}
	// pulling events by event nid is ok
	events, err = table.SelectByNIDs(txn, true, nids)
	if err != nil {
		t.Fatalf("SelectByNIDs failed: %s", err)
	}
	if len(events) != 3 {
		t.Fatalf("SelectByNIDs returned %d events, want 3", len(events))
	}

	// pulling non-existent nids returns no error but a zero slice if verifyAll=false
	events, err = table.SelectByNIDs(txn, false, []int64{9999999})
	if err != nil {
		t.Fatalf("SelectByNIDs failed: %s", err)
	}
	if len(events) != 0 {
		t.Fatalf("SelectByNIDs returned %d events, wanted none", len(events))
	}
	// pulling non-existent nids returns error if verifyAll=true
	events, err = table.SelectByNIDs(txn, true, []int64{9999999})
	if err == nil {
		t.Fatalf("SelectByNIDs did not fail, wanted to")
	}

	// pulling stripped events by NID is ok
	strippedEvents, err := table.SelectStrippedEventsByNIDs(txn, true, nids)
	if err != nil {
		t.Fatalf("SelectStrippedEventsByNIDs failed: %s", err)
	}
	if len(strippedEvents) != 3 {
		t.Fatalf("SelectStrippedEventsByNIDs returned %d events, want 3", len(strippedEvents))
	}
	verifyStripped := func(stripped []Event) {
		wantTypes := []string{"T1", "T2", "T3"}
		wantKeys := []string{"S1", "S2", ""}
		for i := range stripped {
			if stripped[i].Type != wantTypes[i] {
				t.Errorf("Stripped event %d type mismatch: got %s want %s", i, stripped[i].Type, wantTypes[i])
			}
			if stripped[i].StateKey != wantKeys[i] {
				t.Errorf("Stripped event %d state_key mismatch: got %s want %s", i, stripped[i].StateKey, wantKeys[i])
			}
			if stripped[i].NID != nids[i] {
				t.Errorf("Stripped event %d nid mismatch: got %d want %d", i, stripped[i].NID, nids[i])
			}
		}
	}
	verifyStripped(strippedEvents)
	// pulling stripped events by ID is ok
	strippedEvents, err = table.SelectStrippedEventsByIDs(txn, true, []string{"100", "101", "102"})
	if err != nil {
		t.Fatalf("SelectStrippedEventsByIDs failed: %s", err)
	}
	if len(strippedEvents) != 3 {
		t.Fatalf("SelectStrippedEventsByIDs returned %d events, want 3", len(strippedEvents))
	}
	verifyStripped(strippedEvents)
}

func TestEventTableNullValue(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Rollback()
	roomID := "!0:localhost"
	table := NewEventTable(db)
	// `state_key` has a null byte value, but `type` has an escaped literal "\u0000". Ensure the former is culled but the latter is not.
	originalJSON := []byte(`{"event_id":"nullevent", "state_key":"foo", "null":"\u0000", "type": "\\u0000", "room_id":"` + roomID + `"}`)
	events := []Event{
		{
			ID:   "nullevent",
			JSON: originalJSON,
		},
	}
	idToNID, err := table.Insert(txn, events, true)
	if err != nil {
		t.Fatalf("Insert failed: %s", err)
	}
	if len(idToNID) != len(events) {
		t.Fatalf("wanted %d new events, got %d", len(events), len(idToNID))
	}
	gotEvents, err := table.SelectByIDs(txn, true, []string{"nullevent"})
	if err != nil {
		t.Fatalf("SelectByIDs: %s", err)
	}
	if len(gotEvents) != 1 {
		t.Fatalf("SelectByIDs: got %d events want 1", len(gotEvents))
	}
	if gotEvents[0].Type != `\u0000` {
		t.Fatalf(`Escaped null byte didn't survive storage, got %s want \u0000`, gotEvents[0].Type)
	}
	if !bytes.Equal(gotEvents[0].JSON, originalJSON) {
		t.Fatalf("event JSON was modified, \ngot  %v \nwant %v", string(gotEvents[0].JSON), string(originalJSON))
	}
}

func TestEventTableDupeInsert(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	// first insert
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	roomID := "!TestEventTableDupeInsert:localhost"
	table := NewEventTable(db)
	originalJSON := []byte(`{"event_id":"dupeevent", "state_key":"foo", "type":"bar", "room_id":"` + roomID + `"}`)
	events := []Event{
		{
			JSON:   originalJSON,
			RoomID: roomID,
		},
	}
	idToNID, err := table.Insert(txn, events, true)
	if err != nil {
		t.Fatalf("Insert failed: %s", err)
	}
	if len(idToNID) != len(events) {
		t.Fatalf("wanted %d new events, got %d", len(events), len(idToNID))
	}

	// pull out the nid
	nid := idToNID["dupeevent"]
	txn.Commit()

	// now insert again
	txn, err = db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	events = []Event{
		{
			JSON:   originalJSON,
			RoomID: roomID,
		},
	}
	idToNID2, err := table.Insert(txn, events, true)
	if err != nil {
		t.Fatalf("Insert failed: %s", err)
	}
	if len(idToNID2) != 0 {
		t.Fatalf("wanted 0 new events, got %d", len(idToNID2))
	}

	// pull out the nid
	idToNID3, err := table.SelectNIDsByIDs(txn, []string{"dupeevent"})
	if err != nil {
		t.Fatalf("SelectNIDsByIDs: %v", err)
	}
	txn.Commit()

	if nid != int(idToNID3["dupeevent"]) {
		t.Fatalf("nid mismatch, got %v want %v", int(idToNID3["dupeevent"]), nid)
	}
}

func TestEventTableSelectEventsBetween(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	table := NewEventTable(db)
	searchRoomID := "!0TestEventTableSelectEventsBetween:localhost"
	eventIDs := []string{
		"100TestEventTableSelectEventsBetween",
		"101TestEventTableSelectEventsBetween",
		"102TestEventTableSelectEventsBetween",
		"103TestEventTableSelectEventsBetween",
		"104TestEventTableSelectEventsBetween",
	}
	events := []Event{
		{
			JSON: []byte(`{"event_id":"` + eventIDs[0] + `","type": "T1", "state_key":"S1", "room_id":"` + searchRoomID + `"}`),
		},
		{
			JSON: []byte(`{"event_id":"` + eventIDs[1] + `","type": "T2", "state_key":"S2", "room_id":"` + searchRoomID + `"}`),
		},
		{
			JSON: []byte(`{"event_id":"` + eventIDs[2] + `","type": "T3", "state_key":"", "room_id":"` + searchRoomID + `"}`),
		},
		{
			// different room
			JSON: []byte(`{"event_id":"` + eventIDs[3] + `","type": "T4", "state_key":"", "room_id":"!1TestEventTableSelectEventsBetween:localhost"}`),
		},
		{
			JSON: []byte(`{"event_id":"` + eventIDs[4] + `","type": "T5", "state_key":"", "room_id":"` + searchRoomID + `"}`),
		},
	}
	idToNID, err := table.Insert(txn, events, true)
	if err != nil {
		t.Fatalf("Insert failed: %s", err)
	}
	if len(idToNID) != len(events) {
		t.Fatalf("failed to insert events: got %d want %d", len(idToNID), len(events))
	}
	txn.Commit()

	t.Run("subgroup", func(t *testing.T) {
		t.Run("selecting multiple events known lower bound", func(t *testing.T) {
			t.Parallel()
			txn2, err := db.Beginx()
			if err != nil {
				t.Fatalf("failed to start txn: %s", err)
			}
			defer txn2.Rollback()
			events, err := table.SelectByIDs(txn2, true, []string{eventIDs[0]})
			if err != nil || len(events) == 0 {
				t.Fatalf("failed to extract event for lower bound: %s", err)
			}
			events, err = table.SelectEventsBetween(txn2, searchRoomID, int64(events[0].NID), EventsEnd, 1000)
			if err != nil {
				t.Fatalf("failed to SelectEventsBetween: %s", err)
			}
			// 3 as 1 is from a different room
			if len(events) != 3 {
				t.Fatalf("wanted 3 events, got %d", len(events))
			}
		})
		t.Run("selecting multiple events known lower and upper bound", func(t *testing.T) {
			t.Parallel()
			txn3, err := db.Beginx()
			if err != nil {
				t.Fatalf("failed to start txn: %s", err)
			}
			defer txn3.Rollback()
			events, err := table.SelectByIDs(txn3, true, []string{eventIDs[0], eventIDs[2]})
			if err != nil || len(events) == 0 {
				t.Fatalf("failed to extract event for lower/upper bound: %s", err)
			}
			events, err = table.SelectEventsBetween(txn3, searchRoomID, int64(events[0].NID), int64(events[1].NID), 1000)
			if err != nil {
				t.Fatalf("failed to SelectEventsBetween: %s", err)
			}
			// eventIDs[1] and eventIDs[2]
			if len(events) != 2 {
				t.Fatalf("wanted 2 events, got %d", len(events))
			}
		})
		t.Run("selecting multiple events unknown bounds (all events)", func(t *testing.T) {
			t.Parallel()
			txn4, err := db.Beginx()
			if err != nil {
				t.Fatalf("failed to start txn: %s", err)
			}
			defer txn4.Rollback()
			gotEvents, err := table.SelectEventsBetween(txn4, searchRoomID, EventsStart, EventsEnd, 1000)
			if err != nil {
				t.Fatalf("failed to SelectEventsBetween: %s", err)
			}
			// one less as one event is for a different room
			if len(gotEvents) != (len(events) - 1) {
				t.Fatalf("wanted %d events, got %d", len(events)-1, len(gotEvents))
			}
		})
		t.Run("selecting multiple events hitting the limit", func(t *testing.T) {
			t.Parallel()
			txn5, err := db.Beginx()
			if err != nil {
				t.Fatalf("failed to start txn: %s", err)
			}
			defer txn5.Rollback()
			limit := 2
			gotEvents, err := table.SelectEventsBetween(txn5, searchRoomID, EventsStart, EventsEnd, limit)
			if err != nil {
				t.Fatalf("failed to SelectEventsBetween: %s", err)
			}
			if len(gotEvents) != limit {
				t.Fatalf("wanted %d events, got %d", limit, len(gotEvents))
			}
		})
	})
}

func TestEventTableMembershipDetection(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Commit()
	roomID := "!TestEventTableMembershipDetection:localhost"
	alice := "@TestEventTableMembershipDetection_alice:localhost"
	bob := "@TestEventTableMembershipDetection_bob:localhost"
	table := NewEventTable(db)
	events := []Event{
		{
			RoomID: roomID,
			JSON:   testutils.NewJoinEvent(t, alice),
		},
		{
			RoomID: roomID,
			JSON:   testutils.NewStateEvent(t, "m.room.member", bob, alice, map[string]interface{}{"membership": "invite"}),
		},
		{
			RoomID: roomID,
			JSON: testutils.NewJoinEvent(t, alice,
				testutils.WithUnsigned(map[string]interface{}{
					"prev_content": map[string]interface{}{
						"membership": "join",
					},
				}),
			),
		},
	}
	_, err = table.Insert(txn, events, true)
	if err != nil {
		t.Fatalf("failed to insert: %s", err)
	}
	eventIDs := make([]string, len(events))
	for i := range events {
		eventIDs[i] = events[i].ID
	}
	gotEvents, err := table.SelectByIDs(txn, true, eventIDs)
	if err != nil {
		t.Fatalf("failed to select: %s", err)
	}
	wantMemberships := []string{"join", "invite", "_join"}
	for i := range wantMemberships {
		if gotEvents[i].Membership != wantMemberships[i] {
			t.Errorf("event: got membership '%s' want '%s'", gotEvents[i].Membership, wantMemberships[i])
		}
	}
}

func TestChunkify(t *testing.T) {
	// Make 100 dummy events
	events := make([]Event, 100)
	for i := 0; i < len(events); i++ {
		events[i] = Event{
			NID: int64(i),
		}
	}
	eventChunker := EventChunker(events)
	testCases := []struct {
		name             string
		numParamsPerStmt int
		maxParamsPerCall int
		chunkSizes       []int // length = number of chunks wanted, ints = events in that chunk
	}{
		{
			name:             "below chunk limit returns 1 chunk",
			numParamsPerStmt: 3,
			maxParamsPerCall: 400,
			chunkSizes:       []int{100},
		},
		{
			name:             "just above chunk limit returns 2 chunks",
			numParamsPerStmt: 3,
			maxParamsPerCall: 297,
			chunkSizes:       []int{99, 1},
		},
		{
			name:             "way above chunk limit returns many chunks",
			numParamsPerStmt: 3,
			maxParamsPerCall: 30,
			chunkSizes:       []int{10, 10, 10, 10, 10, 10, 10, 10, 10, 10},
		},
		{
			name:             "fractional division rounds down",
			numParamsPerStmt: 3,
			maxParamsPerCall: 298,
			chunkSizes:       []int{99, 1},
		},
		{
			name:             "fractional division rounds down",
			numParamsPerStmt: 3,
			maxParamsPerCall: 299,
			chunkSizes:       []int{99, 1},
		},
	}
	for _, tc := range testCases {
		testCase := tc
		t.Run(testCase.name, func(t *testing.T) {
			chunks := sqlutil.Chunkify(testCase.numParamsPerStmt, testCase.maxParamsPerCall, eventChunker)
			if len(chunks) != len(testCase.chunkSizes) {
				t.Fatalf("got %d chunks, want %d", len(chunks), len(testCase.chunkSizes))
			}
			eventNID := int64(0)
			for i := 0; i < len(chunks); i++ {
				if chunks[i].Len() != testCase.chunkSizes[i] {
					t.Errorf("chunk %d got %d elements, want %d", i, chunks[i].Len(), testCase.chunkSizes[i])
				}
				eventChunk := chunks[i].(EventChunker)
				for j, ev := range eventChunk {
					if ev.NID != eventNID {
						t.Errorf("chunk %d got wrong event in position %d: got NID %d want NID %d", i, j, ev.NID, eventNID)
					}
					eventNID += 1
				}
			}
		})
	}
}

func TestEventTableSelectEventsWithTypeStateKey(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	userID := "@TestEventTableSelectEventsWithTypeStateKey_alice:localhost"
	roomA := "!TestEventTableSelectEventsWithTypeStateKey_A:localhost"
	roomB := "!TestEventTableSelectEventsWithTypeStateKey_B:localhost"
	roomC := "!TestEventTableSelectEventsWithTypeStateKey_C:localhost"
	roomD := "!TestEventTableSelectEventsWithTypeStateKey_D:localhost"
	table := NewEventTable(db)
	_, err = table.Insert(txn, []Event{
		{
			RoomID: roomA,
			JSON:   testutils.NewStateEvent(t, "m.room.create", "", userID, map[string]interface{}{}),
		},
		{
			RoomID: roomB,
			JSON:   testutils.NewStateEvent(t, "m.room.create", "", userID, map[string]interface{}{}),
		},
		{
			RoomID: roomC,
			JSON:   testutils.NewStateEvent(t, "m.room.create", "", userID, map[string]interface{}{}),
		},
		{
			RoomID: roomA,
			JSON:   testutils.NewJoinEvent(t, userID),
		},
		{
			RoomID: roomB,
			JSON:   testutils.NewJoinEvent(t, userID),
		},
		{
			RoomID: roomC,
			JSON:   testutils.NewJoinEvent(t, userID),
		},
		{
			RoomID: roomD,
			JSON:   testutils.NewStateEvent(t, "m.room.member", "@bob:localhost", userID, map[string]interface{}{"membership": "join"}),
		},
	}, true)
	txn.Commit()
	if err != nil {
		t.Fatalf("failed to insert events: %s", err)
	}
	latest, err := table.SelectHighestNID()
	if err != nil {
		t.Fatalf("failed to select highest nid: %s", err)
	}

	gotEvents, err := table.SelectEventsWithTypeStateKey("m.room.member", userID, 0, latest)
	if err != nil {
		t.Fatalf("SelectEventsWithTypeStateKey: %s", err)
	}
	if len(gotEvents) != 3 {
		t.Fatalf("SelectEventsWithTypeStateKey returned %d events, want 3\n%+v", len(gotEvents), gotEvents)
	}
	wantRooms := map[string]bool{
		roomA: true,
		roomB: true,
		roomC: true,
	}
	for _, ev := range gotEvents {
		if !wantRooms[ev.RoomID] {
			t.Errorf("SelectEventsWithTypeStateKey returned unexpected event: %+v", ev)
		}
		delete(wantRooms, ev.RoomID)
	}
	if len(wantRooms) > 0 {
		t.Fatalf("SelectEventsWithTypeStateKey missed rooms: %v", wantRooms)
	}

	gotEvents, err = table.SelectEventsWithTypeStateKeyInRooms([]string{roomA, roomB, roomD}, "m.room.member", userID, 0, latest)
	if err != nil {
		t.Fatalf("SelectEventsWithTypeStateKeyInRooms: %s", err)
	}
	if len(gotEvents) != 2 {
		t.Fatalf("SelectEventsWithTypeStateKeyInRooms returned %d events, want 2", len(gotEvents))
	}
	wantRooms = map[string]bool{
		roomA: true,
		roomB: true,
	}
	for _, ev := range gotEvents {
		if !wantRooms[ev.RoomID] {
			t.Errorf("SelectEventsWithTypeStateKeyInRooms returned unexpected event: %+v", ev)
		}
		delete(wantRooms, ev.RoomID)
	}
	if len(wantRooms) > 0 {
		t.Fatalf("SelectEventsWithTypeStateKeyInRooms missed rooms: %v", wantRooms)
	}
}

// Do a massive insert/select for event IDs (greater than postgres limit) and ensure it works.
func TestTortureEventTable(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	roomID := "!0:localhost"
	table := NewEventTable(db)
	// Insert a ton of events
	events := make([]Event, 10+MaxPostgresParameters)
	eventIDs := make([]string, len(events))
	for i := 0; i < len(events); i++ {
		events[i] = Event{
			ID:     fmt.Sprintf("$%d", i),
			Type:   "my_type",
			RoomID: roomID,
			JSON:   []byte(fmt.Sprintf(`{"type":"my_type","content":{"data":%d}}`, i)),
		}
		eventIDs[i] = events[i].ID
	}
	idToNID, err := table.Insert(txn, events, true)
	if err != nil {
		t.Fatalf("failed to insert %d events: %s", len(events), err)
	}
	if len(idToNID) != len(events) {
		t.Fatalf("only inserted %d/%d events", len(idToNID), len(events))
	}
	if err = txn.Commit(); err != nil {
		t.Fatalf("failed to commit insert")
	}

	// Now do a massive select
	txn, err = db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	nids, err := table.SelectNIDsByIDs(txn, eventIDs)
	if err != nil {
		t.Fatalf("SelectNIDsByIDs: %s", err)
	}
	if len(nids) != len(eventIDs) {
		t.Fatalf("failed to retrieve nids for ids, got %d/%d", len(nids), len(eventIDs))
	}

}

// Test that prev_batch can be inserted and can be intelligently queried.
// 1: can insert events with a prev_batch
// 2: SelectClosestPrevBatch with the event which has the prev_batch field returns that event's prev_batch
// 3: SelectClosestPrevBatch with an event without a prev_batch returns the next newest (stream order) event with a prev_batch
// 4: SelectClosestPrevBatch with an event without a prev_batch returns nothing if there are no newer events with a prev_batch
func TestEventTablePrevBatch(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	roomID1 := "!1:localhost"
	roomID2 := "!2:localhost"
	table := NewEventTable(db)
	// interleave room 1 and room 2 events, and assign prev_batch to some events
	//    ------newer----->
	//   0 1 2 3 4 5 6 7 8 9   index
	//   A B C D E F G H I J   event ID
	//   1 1 2 1 1 2 2 1 2 1   room ID
	//     p p p   p   p       prev batch
	events := []Event{
		{
			ID:     "$A",
			RoomID: roomID1,
			JSON:   []byte(`{"type":"my_type"}`),
		},
		{
			ID:        "$B",
			RoomID:    roomID1,
			JSON:      []byte(`{"type":"my_type"}`),
			PrevBatch: sql.NullString{String: "pb", Valid: true},
		},
		{
			ID:        "$C",
			RoomID:    roomID2,
			JSON:      []byte(`{"type":"my_type"}`),
			PrevBatch: sql.NullString{String: "pc", Valid: true},
		},
		{
			ID:        "$D",
			RoomID:    roomID1,
			JSON:      []byte(`{"type":"my_type"}`),
			PrevBatch: sql.NullString{String: "pd", Valid: true},
		},
		{
			ID:     "$E",
			RoomID: roomID1,
			JSON:   []byte(`{"type":"my_type"}`),
		},
		{
			ID:        "$F",
			RoomID:    roomID2,
			JSON:      []byte(`{"type":"my_type"}`),
			PrevBatch: sql.NullString{String: "pf", Valid: true},
		},
		{
			ID:     "$G",
			RoomID: roomID2,
			JSON:   []byte(`{"type":"my_type"}`),
		},
		{
			ID:        "$H",
			RoomID:    roomID1,
			JSON:      []byte(`{"type":"my_type"}`),
			PrevBatch: sql.NullString{String: "ph", Valid: true},
		},
		{
			ID:     "$I",
			RoomID: roomID2,
			JSON:   []byte(`{"type":"my_type"}`),
		},
		{
			ID:     "$J",
			RoomID: roomID1,
			JSON:   []byte(`{"type":"my_type"}`),
		},
	}
	eventIDs := make([]string, len(events))
	for i := range events {
		eventIDs[i] = events[i].ID
	}

	// 1: can insert events with a prev_batch
	idToNID, err := table.Insert(txn, events, true)
	if err != nil {
		t.Fatalf("failed to insert %d events: %s", len(events), err)
	}
	if len(idToNID) != len(events) {
		t.Fatalf("only inserted %d/%d events", len(idToNID), len(events))
	}
	if err = txn.Commit(); err != nil {
		t.Fatalf("failed to commit insert")
	}

	assertPrevBatch := func(roomID string, index int, wantPrevBatch string) {
		gotPrevBatch, err := table.SelectClosestPrevBatch(roomID, int64(idToNID[events[index].ID]))
		if err != nil {
			t.Fatalf("failed to SelectClosestPrevBatch: %s", err)
		}
		if wantPrevBatch != "" {
			if gotPrevBatch == "" || gotPrevBatch != wantPrevBatch {
				t.Fatalf("SelectClosestPrevBatch: got %v want %v", gotPrevBatch, wantPrevBatch)
			}
		} else if gotPrevBatch != "" {
			t.Fatalf("SelectClosestPrevBatch: got %v want nothing", gotPrevBatch)
		}
		gotPrevBatch, err = table.SelectClosestPrevBatchByID(roomID, eventIDs[index])
		if err != nil {
			t.Fatalf("failed to SelectClosestPrevBatchByID: %s", err)
		}
		if wantPrevBatch != "" {
			if gotPrevBatch == "" || gotPrevBatch != wantPrevBatch {
				t.Fatalf("SelectClosestPrevBatchByID: got %v want %v", gotPrevBatch, wantPrevBatch)
			}
		} else if gotPrevBatch != "" {
			t.Fatalf("SelectClosestPrevBatchByID: got %v want nothing", gotPrevBatch)
		}
	}

	// 2: SelectClosestPrevBatch with the event which has the prev_batch field returns that event's prev_batch
	assertPrevBatch(roomID1, 3, events[3].PrevBatch.String) // returns event D

	// 3: SelectClosestPrevBatch with an event without a prev_batch returns the next newest (stream order) event with a prev_batch
	assertPrevBatch(roomID1, 4, events[7].PrevBatch.String) // query event E, returns event H
	assertPrevBatch(roomID1, 0, events[1].PrevBatch.String) // query event A, returns event B

	// 4: SelectClosestPrevBatch with an event without a prev_batch returns nothing if there are no newer events with a prev_batch
	assertPrevBatch(roomID1, 8, "") // query event I, returns nothing
}

func TestRemoveUnsignedTXNID(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Rollback()
	alice := "@TestRemoveUnsignedTXNID_alice:localhost"
	roomID1 := "!1:localhost"

	events := []Event{
		{ // field should be removed
			ID:     "$A",
			RoomID: roomID1,
			JSON: testutils.NewJoinEvent(t, alice,
				testutils.WithUnsigned(map[string]interface{}{
					"prev_content": map[string]interface{}{
						"membership": "invite",
					},
					"txn_id": "randomTxnID",
				}),
			),
		},
		{ // non-existent field should not result in an error
			ID:     "$B",
			RoomID: roomID1,
			JSON: testutils.NewJoinEvent(t, alice,
				testutils.WithUnsigned(map[string]interface{}{
					"prev_content": map[string]interface{}{
						"membership": "join",
					},
				}),
			),
		},
	}

	table := NewEventTable(db)

	// Insert the events
	_, err = table.Insert(txn, events, false)
	if err != nil {
		t.Errorf("failed to insert event: %s", err)
	}

	// Get the inserted events
	gotEvents, err := table.SelectByIDs(txn, false, []string{"$A", "$B"})
	if err != nil {
		t.Fatalf("failed to select events: %s", err)
	}

	// None of the events should have a `unsigned.txn_id` field
	for _, ev := range gotEvents {
		jsonTXNId := gjson.GetBytes(ev.JSON, "unsigned.txn_id")
		if jsonTXNId.Exists() {
			t.Fatalf("expected unsigned.txn_id to be removed, got '%s'", jsonTXNId.String())
		}
	}
}
