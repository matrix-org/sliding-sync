package state

import (
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sync-v3/sqlutil"
)

func TestEventTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
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
	numNew, err := table.Insert(txn, events)
	if err != nil {
		t.Fatalf("Insert failed: %s", err)
	}
	if numNew != len(events) {
		t.Fatalf("wanted %d new events, got %d", len(events), numNew)
	}
	txn.Commit()

	txn, err = db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Rollback()
	// duplicate insert is ok
	numNew, err = table.Insert(txn, events)
	if err != nil {
		t.Fatalf("Insert failed: %s", err)
	}
	if numNew != 0 {
		t.Fatalf("wanted 0 new events, got %d", numNew)
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
	gotnids, err := table.SelectNIDsByIDs(txn, []string{"100", "101", "102"})
	if err != nil {
		t.Fatalf("SelectNIDsByIDs failed: %s", err)
	}
	if len(gotnids) != 3 {
		t.Fatalf("SelectNIDsByIDs returned %d events, want 3", len(gotnids))
	}

	// set a snapshot ID on them
	var firstSnapshotID int64 = 55
	for _, nid := range gotnids {
		if err = table.UpdateBeforeSnapshotID(txn, nid, firstSnapshotID, 0); err != nil {
			t.Fatalf("UpdateSnapshotID: %s", err)
		}
	}
	// query the snapshot
	lastEventNID, _, snapID, err := table.BeforeStateSnapshotIDForEventNID(txn, roomID, gotnids[1])
	if err != nil {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: %s", err)
	}
	if snapID != firstSnapshotID {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: got snap ID %d want %d", snapID, firstSnapshotID)
	}
	// the queried position
	if lastEventNID != gotnids[1] {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: didn't return last inserted event, got %d want %d", lastEventNID, gotnids[1])
	}
	// try again with a much higher pos
	lastEventNID, _, snapID, err = table.BeforeStateSnapshotIDForEventNID(txn, roomID, 999999)
	if err != nil {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: %s", err)
	}
	if snapID != firstSnapshotID {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: got snap ID %d want %d", snapID, firstSnapshotID)
	}
	if lastEventNID != gotnids[2] {
		t.Fatalf("BeforeStateSnapshotIDForEventNID: didn't return last inserted event, got %d want %d", lastEventNID, gotnids[2])
	}

	// find max and query it
	var wantHighestNID int64
	for _, nid := range gotnids {
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

func TestEventTableSelectEventsBetween(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
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
	numNew, err := table.Insert(txn, events)
	if err != nil {
		t.Fatalf("Insert failed: %s", err)
	}
	if numNew != len(events) {
		t.Fatalf("failed to insert events: got %d want %d", numNew, len(events))
	}
	txn.Commit()

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
