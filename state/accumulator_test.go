package state

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"github.com/matrix-org/sliding-sync/sync2"
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
	db, close := connectToDB(t)
	defer close()
	accumulator := NewAccumulator(db)
	res, err := accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("falied to Initialise accumulator: %s", err)
	}
	if !res.AddedEvents {
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
	if snapID != res.SnapshotID {
		t.Fatalf("Initialise returned wrong snapshot ID, got %v want %v", res.SnapshotID, snapID)
	}

	// this snapshot should have 1 member event and 2 other events in it
	row, err := accumulator.snapshotTable.Select(txn, snapID)
	if err != nil {
		t.Fatalf("failed to select snapshot %d: %s", snapID, err)
	}
	if len(row.MembershipEvents) != 1 {
		t.Fatalf("got %d membership events, want %d in current state snapshot", len(row.MembershipEvents), 1)
	}
	if len(row.OtherEvents) != 2 {
		t.Fatalf("got %d other events, want %d in current state snapshot", len(row.MembershipEvents), 2)
	}

	// these 3 events should map to the three events we initialised with
	events, err := accumulator.eventsTable.SelectByNIDs(txn, true, append(row.OtherEvents, row.MembershipEvents...))
	if err != nil {
		t.Fatalf("failed to extract events in snapshot: %s", err)
	}
	if len(events) != len(roomEvents) {
		t.Fatalf("failed to extract %d events, got %d", len(roomEvents), len(events))
	}
	// sort alphabetically on ID like roomEventIDs
	sort.Slice(events, func(i, j int) bool {
		return events[i].ID < events[j].ID
	})
	for i := range events {
		if events[i].ID != roomEventIDs[i] {
			t.Errorf("event %d was not stored correctly: got ID %s want %s", i, events[i].ID, roomEventIDs[i])
		}
	}

	// Subsequent calls do nothing and are not an error
	res, err = accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("falied to Initialise accumulator: %s", err)
	}
	if res.AddedEvents {
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
	db, close := connectToDB(t)
	defer close()
	accumulator := NewAccumulator(db)
	_, err := accumulator.Initialise(roomID, roomEvents)
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
	err = sqlutil.WithTransaction(accumulator.db, func(txn *sqlx.Tx) error {
		numNew, latestNIDs, err = accumulator.Accumulate(txn, roomID, "", newEvents)
		return err
	})
	if err != nil {
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
	wantOtherStateEvents := []json.RawMessage{
		roomEvents[0], // create event
		newEvents[1],  // join rules
		newEvents[2],  // history visibility
	}
	wantMemberEvents := []json.RawMessage{
		roomEvents[1],
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
	if len(row.MembershipEvents) != len(wantMemberEvents) {
		t.Fatalf("snapshot: %d got %d member events, want %d in current state snapshot", snapID, len(row.MembershipEvents), len(wantMemberEvents))
	}
	if len(row.OtherEvents) != len(wantOtherStateEvents) {
		t.Fatalf("snapshot: %d got %d other events, want %d in current state snapshot", snapID, len(row.OtherEvents), len(wantOtherStateEvents))
	}

	// these 4 events should map to the create/member events from initialise, then the join_rules/history_visibility from accumulate
	events, err := accumulator.eventsTable.SelectByNIDs(txn, true, append(row.OtherEvents, row.MembershipEvents...))
	if err != nil {
		t.Fatalf("failed to extract events in snapshot: %s", err)
	}
	wantStateEvents := append(wantOtherStateEvents, wantMemberEvents...)
	if len(events) != len(wantStateEvents) {
		t.Fatalf("failed to extract %d events, got %d", len(wantStateEvents), len(events))
	}
	sort.Slice(wantStateEvents, func(i, j int) bool {
		return gjson.ParseBytes(wantStateEvents[i]).Get("event_id").Str < gjson.ParseBytes(wantStateEvents[j]).Get("event_id").Str
	})
	for i := range events {
		if events[i].ID != gjson.GetBytes(wantStateEvents[i], "event_id").Str {
			t.Errorf("event %d was not stored correctly: got ID %s want %s", i, events[i].ID, gjson.GetBytes(wantStateEvents[i], "event_id").Str)
		}
	}

	// subsequent calls do nothing and are not an error
	err = sqlutil.WithTransaction(accumulator.db, func(txn *sqlx.Tx) error {
		_, _, err = accumulator.Accumulate(txn, roomID, "", newEvents)
		return err
	})
	if err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}
}

func TestAccumulatorMembershipLogs(t *testing.T) {
	roomID := "!TestAccumulatorMembershipLogs:localhost"
	db, close := connectToDB(t)
	defer close()
	accumulator := NewAccumulator(db)
	_, err := accumulator.Initialise(roomID, nil)
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
	err = sqlutil.WithTransaction(accumulator.db, func(txn *sqlx.Tx) error {
		_, _, err = accumulator.Accumulate(txn, roomID, "", roomEvents)
		return err
	})
	if err != nil {
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

	db, close := connectToDB(t)
	defer close()
	accumulator := NewAccumulator(db)
	roomID := "!buggy:localhost"
	_, err := accumulator.Initialise(roomID, joinRoom.State.Events)
	if err != nil {
		t.Fatalf("failed to Initialise accumulator: %s", err)
	}

	err = sqlutil.WithTransaction(accumulator.db, func(txn *sqlx.Tx) error {
		_, _, err = accumulator.Accumulate(txn, roomID, "", joinRoom.Timeline.Events)
		return err
	})
	if err != nil {
		t.Fatalf("failed to Accumulate: %s", err)
	}
}

// Regression test for corrupt state snapshots.
// This seems to have happened in the wild, whereby the snapshot exhibited 2 things:
//   - A message event having a event_replaces_nid. This should be impossible as messages are not state.
//   - Duplicate events in the state snapshot.
//
// We can reproduce duplicate events in the state snapshot by doing the following:
//
//	-
//
// This can then be tested by:
//   - Query the current room snapshot.
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
	db, close := connectToDB(t)
	defer close()
	testCases := []struct {
		input          StrippedEvents
		inputEvent     Event
		wantMemberNIDs []int64
		wantOtherNIDs  []int64
		wantErr        bool
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
			wantOtherNIDs: []int64{2},
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
			wantOtherNIDs: []int64{1, 2},
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
		{
			input: StrippedEvents{
				{
					NID:      1,
					Type:     "m.room.member",
					StateKey: "alice",
				},
				{
					NID:      2,
					Type:     "m.room.member",
					StateKey: "bob",
				},
				{
					NID:      3,
					Type:     "other",
					StateKey: "",
				},
			},
			inputEvent: Event{
				NID:      4,
				Type:     "m.room.member",
				StateKey: "alice",
			},
			wantMemberNIDs: []int64{4, 2},
			wantOtherNIDs:  []int64{3},
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
		gotMemberNIDs, gotOtherNIDs := got.NIDs()
		assertNIDsEqual(gotMemberNIDs, tc.wantMemberNIDs)
		assertNIDsEqual(gotOtherNIDs, tc.wantOtherNIDs)
	}
}

// Test that you can accumulate the same room with the same partial sequence of timeline events and
// state is updated correctly. This relies on postgres blocking subsequent transactions sensibly.
func TestAccumulatorConcurrency(t *testing.T) {
	roomID := "!TestAccumulatorConcurrency:localhost"
	roomEvents := []json.RawMessage{
		[]byte(`{"event_id":"AA", "type":"m.room.create", "state_key":"", "content":{"creator":"@me:localhost"}}`),
		[]byte(`{"event_id":"BB", "type":"m.room.member", "state_key":"@me:localhost", "content":{"membership":"join"}}`),
		[]byte(`{"event_id":"CC", "type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"public"}}`),
	}
	db, close := connectToDB(t)
	defer close()
	accumulator := NewAccumulator(db)
	_, err := accumulator.Initialise(roomID, roomEvents)
	if err != nil {
		t.Fatalf("failed to Initialise accumulator: %s", err)
	}

	// spin off N goroutines to insert 1,2,3,4,5 in varying batches which can appear in the wild e.g [1,2] [1,2,3,4]
	// we update the room name in all events so we should end up with Name: 5.
	newEvents := []json.RawMessage{
		[]byte(`{"event_id":"con_1", "type":"m.room.name", "state_key":"", "content":{"name":"1"}}`),
		[]byte(`{"event_id":"con_2", "type":"m.room.name", "state_key":"", "content":{"name":"2"}}`),
		[]byte(`{"event_id":"con_3", "type":"m.room.name", "state_key":"", "content":{"name":"3"}}`),
		[]byte(`{"event_id":"con_4", "type":"m.room.name", "state_key":"", "content":{"name":"4"}}`),
		[]byte(`{"event_id":"con_5", "type":"m.room.name", "state_key":"", "content":{"name":"5"}}`),
	}
	totalNumNew := 0
	var wg sync.WaitGroup
	wg.Add(len(newEvents))
	for i := 0; i < len(newEvents); i++ {
		go func(i int) {
			defer wg.Done()
			subset := newEvents[:(i + 1)] // i=0 => [1], i=1 => [1,2], etc
			err := sqlutil.WithTransaction(accumulator.db, func(txn *sqlx.Tx) error {
				numNew, _, err := accumulator.Accumulate(txn, roomID, "", subset)
				totalNumNew += numNew
				return err
			})
			if err != nil {
				t.Errorf("goroutine failed to accumulate: %s", err)
			}
		}(i)
	}
	wg.Wait() // wait for all goroutines to finish
	if totalNumNew != len(newEvents) {
		t.Errorf("got %d total new events, want %d", totalNumNew, len(newEvents))
	}
	// check that the name of the room is "5"
	snapshot := currentSnapshotNIDs(t, accumulator.snapshotTable, roomID)
	t.Logf("snapshot nids: %v", snapshot)
	var events []Event
	err = sqlutil.WithTransaction(accumulator.db, func(txn *sqlx.Tx) error {
		events, err = accumulator.eventsTable.SelectByNIDs(txn, true, snapshot)
		return err
	})
	// find the room name and check it is 5
	roomName := ""
	for _, ev := range events {
		if ev.Type == "m.room.name" {
			roomName = gjson.GetBytes(ev.JSON, "content.name").Str
		}
	}
	if roomName != "5" {
		t.Fatalf("got room name %s want '5'", roomName)
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
