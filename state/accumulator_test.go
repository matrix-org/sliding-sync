package state

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/tidwall/gjson"
)

func TestAccumulatorInitialise(t *testing.T) {
	roomID := "!TestAccumulatorInitialise:localhost"
	roomEvents := []json.RawMessage{
		[]byte(`{"event_id":"A", "type":"m.room.create", "state_key":"", "content":{"creator":"@me:localhost"}}`),
		[]byte(`{"event_id":"B", "type":"m.room.member", "state_key":"@me:localhost", "content":{"membership":"join"}}`),
		[]byte(`{"event_id":"C", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
	}
	roomEventIDs := []string{"A", "B", "C"}
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	accumulator := NewAccumulator(db)
	added, initSnapID, err := accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("falied to Initialise accumulator: %s", err)
	}
	if !added {
		t.Fatalf("didn't add events, wanted it to")
	}

	txn, err := accumulator.db.Beginx()
	if err != nil {
		t.Fatalf("failed to start assert txn: %s", err)
	}
	defer txn.Rollback()

	// There should be one snapshot on the current state
	snapID, err := accumulator.roomsTable.CurrentAfterSnapshotID(txn, roomID)
	if err != nil {
		t.Fatalf("failed to select current snapshot: %s", err)
	}
	if snapID == 0 {
		t.Fatalf("Initialise did not store a current snapshot")
	}
	if snapID != initSnapID {
		t.Fatalf("Initialise returned wrong snapshot ID, got %v want %v", initSnapID, snapID)
	}

	// this snapshot should have 3 events in it
	row, err := accumulator.snapshotTable.Select(txn, snapID)
	if err != nil {
		t.Fatalf("failed to select snapshot %d: %s", snapID, err)
	}
	if len(row.Events) != len(roomEvents) {
		t.Fatalf("got %d events, want %d in current state snapshot", len(row.Events), len(roomEvents))
	}

	// these 3 events should map to the three events we initialised with
	events, err := accumulator.eventsTable.SelectByNIDs(txn, true, row.Events)
	if err != nil {
		t.Fatalf("failed to extract events in snapshot: %s", err)
	}
	if len(events) != len(roomEvents) {
		t.Fatalf("failed to extract %d events, got %d", len(roomEvents), len(events))
	}
	for i := range events {
		if events[i].ID != roomEventIDs[i] {
			t.Errorf("event %d was not stored correctly: got ID %s want %s", i, events[i].ID, roomEventIDs[i])
		}
	}

	// Subsequent calls do nothing and are not an error
	added, _, err = accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("falied to Initialise accumulator: %s", err)
	}
	if added {
		t.Fatalf("added events when it shouldn't have")
	}
}

func TestAccumulatorAccumulate(t *testing.T) {
	roomID := "!TestAccumulatorAccumulate:localhost"
	roomEvents := []json.RawMessage{
		[]byte(`{"event_id":"D", "type":"m.room.create", "state_key":"", "content":{"creator":"@me:localhost"}}`),
		[]byte(`{"event_id":"E", "type":"m.room.member", "state_key":"@me:localhost", "content":{"membership":"join"}}`),
		[]byte(`{"event_id":"F", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
	}
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	accumulator := NewAccumulator(db)
	_, _, err = accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("failed to Initialise accumulator: %s", err)
	}

	// accumulate new state makes a new snapshot and removes the old snapshot
	newEvents := []json.RawMessage{
		// non-state event does nothing
		[]byte(`{"event_id":"G", "type":"m.room.message","content":{"body":"Hello World","msgtype":"m.text"}}`),
		// join_rules should clobber the one from initialise
		[]byte(`{"event_id":"H", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
		// new state event should be added to the snapshot
		[]byte(`{"event_id":"I", "type":"m.room.history_visibility", "state_key":"", "content":{"visibility":"public"}}`),
	}
	var numNew int
	var latestNIDs []int64
	if numNew, latestNIDs, err = accumulator.Accumulate(roomID, "", newEvents); err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}
	if numNew != len(newEvents) {
		t.Fatalf("got %d new events, want %d", numNew, len(newEvents))
	}
	// latest nid shoould match
	wantLatestNID, err := accumulator.eventsTable.SelectHighestNID()
	if err != nil {
		t.Fatalf("failed to check latest NID from Accumulate: %s", err)
	}
	if latestNIDs[len(latestNIDs)-1] != wantLatestNID {
		t.Errorf("Accumulator.Accumulate returned latest nid %d, want %d", latestNIDs[len(latestNIDs)-1], wantLatestNID)
	}

	// Begin assertions
	wantStateEvents := []json.RawMessage{
		roomEvents[0], // create event
		roomEvents[1], // member event
		newEvents[1],  // join rules
		newEvents[2],  // history visibility
	}
	txn, err := accumulator.db.Beginx()
	if err != nil {
		t.Fatalf("failed to start assert txn: %s", err)
	}
	defer txn.Rollback()

	// There should be one snapshot in current state
	snapID, err := accumulator.roomsTable.CurrentAfterSnapshotID(txn, roomID)
	if err != nil {
		t.Fatalf("failed to select current snapshot: %s", err)
	}
	if snapID == 0 {
		t.Fatalf("Initialise or Accumulate did not store a current snapshot")
	}

	// The snapshot should have 4 events
	row, err := accumulator.snapshotTable.Select(txn, snapID)
	if err != nil {
		t.Fatalf("failed to select snapshot %d: %s", snapID, err)
	}
	if len(row.Events) != len(wantStateEvents) {
		t.Fatalf("snapshot: %d got %d events, want %d in current state snapshot", snapID, len(row.Events), len(wantStateEvents))
	}

	// these 4 events should map to the create/member events from initialise, then the join_rules/history_visibility from accumulate
	events, err := accumulator.eventsTable.SelectByNIDs(txn, true, row.Events)
	if err != nil {
		t.Fatalf("failed to extract events in snapshot: %s", err)
	}
	if len(events) != len(wantStateEvents) {
		t.Fatalf("failed to extract %d events, got %d", len(wantStateEvents), len(events))
	}
	for i := range events {
		if events[i].ID != gjson.GetBytes(wantStateEvents[i], "event_id").Str {
			t.Errorf("event %d was not stored correctly: got ID %s want %s", i, events[i].ID, gjson.GetBytes(wantStateEvents[i], "event_id").Str)
		}
	}

	// subsequent calls do nothing and are not an error
	if _, _, err = accumulator.Accumulate(roomID, "", newEvents); err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}
}

func TestAccumulatorDelta(t *testing.T) {
	roomID := "!TestAccumulatorDelta:localhost"
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	accumulator := NewAccumulator(db)
	_, _, err = accumulator.Initialise(roomID, nil)
	if err != nil {
		t.Fatalf("failed to Initialise accumulator: %s", err)
	}
	roomEvents := []json.RawMessage{
		[]byte(`{"event_id":"aD", "type":"m.room.create", "state_key":"", "content":{"creator":"@TestAccumulatorDelta:localhost"}}`),
		[]byte(`{"event_id":"aE", "type":"m.room.member", "state_key":"@TestAccumulatorDelta:localhost", "content":{"membership":"join"}}`),
		[]byte(`{"event_id":"aF", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
		[]byte(`{"event_id":"aG", "type":"m.room.message","content":{"body":"Hello World","msgtype":"m.text"}}`),
		[]byte(`{"event_id":"aH", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
		[]byte(`{"event_id":"aI", "type":"m.room.history_visibility", "state_key":"", "content":{"visibility":"public"}}`),
	}
	if _, _, err = accumulator.Accumulate(roomID, "", roomEvents); err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}

	// Draw the create event, tests limits
	events, position, err := accumulator.Delta(roomID, EventsStart, 1)
	if err != nil {
		t.Fatalf("failed to Delta: %s", err)
	}
	if len(events) != 1 {
		t.Fatalf("failed to get events from Delta, got %d want 1", len(events))
	}
	if gjson.GetBytes(events[0], "event_id").Str != gjson.GetBytes(roomEvents[0], "event_id").Str {
		t.Fatalf("failed to draw first event, got %s want %s", string(events[0]), string(roomEvents[0]))
	}
	if position == 0 {
		t.Errorf("Delta returned zero position")
	}

	// Draw up to the end
	events, position, err = accumulator.Delta(roomID, position, 1000)
	if err != nil {
		t.Fatalf("failed to Delta: %s", err)
	}
	if len(events) != len(roomEvents)-1 {
		t.Fatalf("failed to get events from Delta, got %d want %d", len(events), len(roomEvents)-1)
	}
	if position == 0 {
		t.Errorf("Delta returned zero position")
	}
}

func TestAccumulatorMembershipLogs(t *testing.T) {
	roomID := "!TestAccumulatorMembershipLogs:localhost"
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	accumulator := NewAccumulator(db)
	_, _, err = accumulator.Initialise(roomID, nil)
	if err != nil {
		t.Fatalf("failed to Initialise accumulator: %s", err)
	}
	roomEventIDs := []string{
		"b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8",
	}
	roomEvents := []json.RawMessage{
		[]byte(`{"event_id":"` + roomEventIDs[0] + `", "type":"m.room.create", "state_key":"", "content":{"creator":"@me:localhost"}}`),
		// @me joins
		[]byte(`{"event_id":"` + roomEventIDs[1] + `", "type":"m.room.member", "state_key":"@me:localhost", "content":{"membership":"join"}}`),
		[]byte(`{"event_id":"` + roomEventIDs[2] + `", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
		// @alice joins
		[]byte(`{"event_id":"` + roomEventIDs[3] + `", "type":"m.room.member", "state_key":"@alice:localhost", "content":{"membership":"join"}}`),
		[]byte(`{"event_id":"` + roomEventIDs[4] + `", "type":"m.room.message","content":{"body":"Hello World","msgtype":"m.text"}}`),
		// @me changes display name
		[]byte(`{"event_id":"` + roomEventIDs[5] + `", "type":"m.room.member", "state_key":"@me:localhost","unsigned":{"prev_content":{"membership":"join"}}, "content":{"membership":"join", "displayname":"Me"}}`),
		// @me invites @bob
		[]byte(`{"event_id":"` + roomEventIDs[6] + `", "type":"m.room.member", "state_key":"@bob:localhost", "content":{"membership":"invite"}, "sender":"@me:localhost"}`),
		// @me leaves the room
		[]byte(`{"event_id":"` + roomEventIDs[7] + `", "type":"m.room.member", "state_key":"@me:localhost","unsigned":{"prev_content":{"membership":"join", "displayname":"Me"}}, "content":{"membership":"leave"}}`),
	}
	if _, _, err = accumulator.Accumulate(roomID, "", roomEvents); err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}
	txn, err := accumulator.db.Beginx()
	if err != nil {
		t.Fatalf("failed to start assert txn: %s", err)
	}

	// Begin assertions

	// Pull nids for these events
	insertedEvents, err := accumulator.eventsTable.SelectByIDs(txn, true, roomEventIDs)
	txn.Rollback()
	if err != nil {
		t.Fatalf("Failed to select accumulated events: %s", err)
	}
	startIndex := insertedEvents[0].NID - 1 // lowest index for the inserted events
	testCases := []struct {
		startExcl int64
		endIncl   int64
		target    string
		wantNIDs  []int64
	}{
		{
			startExcl: startIndex,
			endIncl:   int64(insertedEvents[len(insertedEvents)-1].NID),
			target:    "@me:localhost",
			// join then profile change then  leave
			wantNIDs: []int64{insertedEvents[1].NID, insertedEvents[5].NID, insertedEvents[7].NID},
		},
		{
			startExcl: startIndex,
			endIncl:   int64(insertedEvents[len(insertedEvents)-1].NID),
			target:    "@bob:localhost",
			// invite
			wantNIDs: []int64{insertedEvents[6].NID},
		},
		{
			startExcl: int64(insertedEvents[2].NID),
			endIncl:   int64(insertedEvents[6].NID),
			target:    "@me:localhost",
			// profile change only
			wantNIDs: []int64{insertedEvents[5].NID},
		},
	}
	for _, tc := range testCases {
		gotEvents, err := accumulator.eventsTable.SelectEventsWithTypeStateKey(
			"m.room.member", tc.target, tc.startExcl, tc.endIncl,
		)
		if err != nil {
			t.Fatalf("failed to MembershipsBetween: %s", err)
		}
		gotNIDs := make([]int64, len(gotEvents))
		for i := range gotEvents {
			gotNIDs[i] = gotEvents[i].NID
		}
		if !reflect.DeepEqual(gotNIDs, tc.wantNIDs) {
			t.Errorf("MembershipsBetween(%d,%d) got wrong nids, got %v want %v", tc.startExcl, tc.endIncl, gotNIDs, tc.wantNIDs)
		}
	}
}

// This was a bug in the wild on M's account, where the same state event is present in state/timeline
// This is a bug in Synapse, but it shouldn't cause us to fall over.
func TestAccumulatorDupeEvents(t *testing.T) {
	data := `
	{
		"state": {
			"events": [{
				"content": {
					"foo": "bar"
				},
				"origin_server_ts": 1632840448390,
				"sender": "@alice:localhost",
				"state_key": "something",
				"type": "dupe",
				"event_id": "$b"
			}]
		},
		"timeline": {
			"events": [{
				"content": {
					"body": "foo"
				},
				"origin_server_ts": 1631606550669,
				"sender": "@alice:localhost",
				"type": "first",
				"event_id": "$a"
			}, {
				"content": {
					"key": "unimportant"
				},
				"origin_server_ts": 1632132357344,
				"sender": "@alice:localhost",
				"state_key": "anything",
				"type": "second",
				"event_id": "$c"
			}, {
				"content": {
					"foo": "bar"
				},
				"origin_server_ts": 1632840448390,
				"sender": "@alice:localhost",
				"state_key": "something",
				"type": "dupe",
				"event_id": "$b"
			}]
		}
	}`
	var joinRoom sync2.SyncV2JoinResponse
	if err := json.Unmarshal([]byte(data), &joinRoom); err != nil {
		t.Fatalf("failed to unmarshal: %s", err)
	}

	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	accumulator := NewAccumulator(db)
	roomID := "!buggy:localhost"
	_, _, err = accumulator.Initialise(roomID, joinRoom.State.Events)
	if err != nil {
		t.Fatalf("failed to Initialise accumulator: %s", err)
	}

	_, _, err = accumulator.Accumulate(roomID, "", joinRoom.Timeline.Events)
	if err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}
}

// Regression test for corrupt state snapshots.
// This seems to have happened in the wild, whereby the snapshot exhibited 2 things:
//  - A message event having a event_replaces_nid. This should be impossible as messages are not state.
//  - Duplicate events in the state snapshot.
// We can reproduce a message event having a event_replaces_nid by doing the following:
//  - Create a room with initial state A,C
//  - Accumulate events D, A, B(msg). This should be impossible because we already got A initially but whatever, roll with it, blame state resets or something.
//  - This leads to A,B being processed and D ignored if you just take the newest results.
// This can then be tested by:
//  - Query the current room snapshot. This will include B(msg) when it shouldn't.
func TestAccumulatorMisorderedGraceful(t *testing.T) {
	alice := "@alice:localhost"
	bob := "@bob:localhost"

	eventA := testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"})
	eventC := testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{})
	eventD := testutils.NewStateEvent(
		t, "m.room.member", bob, "join", map[string]interface{}{"membership": "join"},
	)
	eventBMsg := testutils.NewEvent(
		t, "m.room.message", bob, map[string]interface{}{"body": "hello"},
	)
	t.Logf("A=member-alice, B=msg, C=create, D=member-bob")

	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	accumulator := NewAccumulator(db)
	roomID := "!TestAccumulatorStateReset:localhost"
	// Create a room with initial state A,C
	_, _, err = accumulator.Initialise(roomID, []json.RawMessage{
		eventA, eventC,
	})
	if err != nil {
		t.Fatalf("failed to Initialise accumulator: %s", err)
	}

	// Accumulate events D, A, B(msg).
	_, _, err = accumulator.Accumulate(roomID, "", []json.RawMessage{eventD, eventA, eventBMsg})
	if err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}

	eventIDs := []string{
		gjson.GetBytes(eventA, "event_id").Str,
		gjson.GetBytes(eventBMsg, "event_id").Str,
		gjson.GetBytes(eventC, "event_id").Str,
		gjson.GetBytes(eventD, "event_id").Str,
	}
	t.Logf("Events A,B,C,D: %v", eventIDs)
	txn := accumulator.db.MustBeginTx(context.Background(), nil)
	idsToNIDs, err := accumulator.eventsTable.SelectNIDsByIDs(txn, eventIDs)
	if err != nil {
		t.Fatalf("Failed to SelectNIDsByIDs: %s", err)
	}
	if len(idsToNIDs) != len(eventIDs) {
		t.Errorf("SelectNIDsByIDs: asked for %v got %v", eventIDs, idsToNIDs)
	}
	t.Logf("Events: %v", idsToNIDs)

	wantEventNIDs := []int64{
		idsToNIDs[eventIDs[0]], idsToNIDs[eventIDs[2]], idsToNIDs[eventIDs[3]],
	}
	sort.Slice(wantEventNIDs, func(i, j int) bool {
		return wantEventNIDs[i] < wantEventNIDs[j]
	})
	// Query the current room snapshot
	gotSnapshotEvents := currentSnapshotNIDs(t, accumulator.snapshotTable, roomID)
	if len(gotSnapshotEvents) != len(wantEventNIDs) { // events A,C,D
		t.Errorf("corrupt snapshot, got %v want %v", gotSnapshotEvents, wantEventNIDs)
	}
	if !reflect.DeepEqual(wantEventNIDs, gotSnapshotEvents) {
		t.Errorf("got %v want %v", gotSnapshotEvents, wantEventNIDs)
	}
}

// Regression test for corrupt state snapshots.
// This seems to have happened in the wild, whereby the snapshot exhibited 2 things:
//  - A message event having a event_replaces_nid. This should be impossible as messages are not state.
//  - Duplicate events in the state snapshot.
// We can reproduce duplicate events in the state snapshot by doing the following:
//  -
// This can then be tested by:
//  - Query the current room snapshot.
func TestCalculateNewSnapshotDupe(t *testing.T) {
	assertNIDsEqual := func(a, b []int64) {
		t.Helper()
		sort.Slice(a, func(i, j int) bool {
			return a[i] < a[j]
		})
		sort.Slice(b, func(i, j int) bool {
			return b[i] < b[j]
		})
		if !reflect.DeepEqual(a, b) {
			t.Errorf("assertNIDsEqual: got %v want %v", a, b)
		}
	}
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	testCases := []struct {
		input      StrippedEvents
		inputEvent Event
		wantNIDs   []int64
		wantErr    bool
	}{
		// basic replace
		{
			input: StrippedEvents{
				{
					NID:      1,
					Type:     "a",
					StateKey: "b",
				},
			},
			inputEvent: Event{
				NID:      2,
				Type:     "a",
				StateKey: "b",
			},
			wantNIDs: []int64{2},
		},
		// basic addition
		{
			input: StrippedEvents{
				{
					NID:      1,
					Type:     "a",
					StateKey: "b",
				},
			},
			inputEvent: Event{
				NID:      2,
				Type:     "c",
				StateKey: "d",
			},
			wantNIDs: []int64{1, 2},
		},
		// dupe nid
		{
			input: StrippedEvents{
				{
					NID:      1,
					Type:     "a1",
					StateKey: "b1",
				},
				{
					NID:      2,
					Type:     "a2",
					StateKey: "b2",
				},
				{
					NID:      3,
					Type:     "a3",
					StateKey: "b3",
				},
			},
			inputEvent: Event{
				NID:      1,
				Type:     "a2",
				StateKey: "b2",
			},
			wantErr: true,
		},
		// regression test from in the wild
		{
			input: StrippedEvents{
				{
					NID:      1,
					Type:     "m.room.member",
					StateKey: "alice", // this was an invite
				},
				{
					NID:      2,
					Type:     "m.room.member",
					StateKey: "alice", // this was a join - buggy because of the failures tested in TestAccumulatorMisorderedGraceful
				},
			},
			inputEvent: Event{
				NID:      3,
				Type:     "m.room.member",
				StateKey: "alice", // another invite
			},
			wantErr: true,
		},
	}
	accumulator := NewAccumulator(db)
	for _, tc := range testCases {
		got, _, err := accumulator.calculateNewSnapshot(tc.input, tc.inputEvent)
		if err != nil {
			if tc.wantErr {
				continue
			}
			t.Errorf("got error %s", err)
			continue
		} else if tc.wantErr {
			t.Errorf("wanted error but got none, instead got %v", got)
			continue
		}
		assertNIDsEqual(got.NIDs(), tc.wantNIDs)
	}
}

func currentSnapshotNIDs(t *testing.T, snapshotTable *SnapshotTable, roomID string) []int64 {
	txn := snapshotTable.db.MustBeginTx(context.Background(), nil)
	defer txn.Commit()
	roomToSnapshotEvents, err := snapshotTable.CurrentSnapshots(txn)
	if err != nil {
		t.Errorf("currentSnapshotNIDs: %s", err)
	}
	events := roomToSnapshotEvents[roomID]
	if len(events) == 0 {
		t.Fatalf("no snapshot events for room %v", roomID)
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i] < events[j]
	})
	return events
}
