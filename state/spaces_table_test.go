package state

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/jmoiron/sqlx"
)

func matchAnyOrder(t *testing.T, gots, wants []SpaceRelation) {
	t.Helper()
	key := func(sr SpaceRelation) string {
		return fmt.Sprintf("%+v", sr)
	}
	sort.Slice(gots, func(i, j int) bool {
		return key(gots[i]) < key(gots[j])
	})
	sort.Slice(wants, func(i, j int) bool {
		return key(wants[i]) < key(wants[j])
	})
	if !reflect.DeepEqual(gots, wants) {
		t.Fatalf("mismatch, got %+v want %+v", gots, wants)
	}
}

func noError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	t.Fatalf(err.Error())
}

func TestSpacesTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Rollback()
	table := NewSpacesTable(db)

	parent := "!parent"
	child1 := "!child1"
	child2 := "!child2"

	// basic insert
	s1 := SpaceRelation{
		Parent:   parent,
		Child:    child1,
		Relation: RelationMSpaceChild,
	}
	s2 := SpaceRelation{
		Parent:      parent,
		Child:       child2,
		Relation:    RelationMSpaceChild,
		Ordering:    "abc",
		IsSuggested: true,
	}
	if err := table.BulkInsert(txn, []SpaceRelation{s1, s2}); err != nil {
		t.Fatalf("BulkInsert: %s", err)
	}

	// basic select
	result, err := table.SelectChildren(txn, []string{parent})
	if err != nil {
		t.Fatalf("SelectChildren: %s", err)
	}
	children, ok := result[parent]
	if !ok || len(children) != 2 {
		t.Fatalf("SelectChildren: want 2 children, got %+v", result)
	}
	matchAnyOrder(t, children, []SpaceRelation{s1, s2})

	// basic update
	s1.Ordering = "xyz"
	s1.IsSuggested = true
	if err := table.BulkInsert(txn, []SpaceRelation{s1}); err != nil {
		t.Fatalf("BulkInsert: %s", err)
	}
	result, err = table.SelectChildren(txn, []string{parent})
	if err != nil {
		t.Fatalf("SelectChildren: %s", err)
	}
	children, ok = result[parent]
	if !ok || len(children) != 2 {
		t.Fatalf("SelectChildren: want 2 children, got %+v", result)
	}
	matchAnyOrder(t, children, []SpaceRelation{s1, s2})

	// basic delete
	if err = table.BulkDelete(txn, []SpaceRelation{s1, s2}); err != nil {
		t.Fatalf("BulkDelete: %s", err)
	}
	result, err = table.SelectChildren(txn, []string{parent})
	if err != nil {
		t.Fatalf("SelectChildren: %s", err)
	}
	if len(result) > 0 {
		t.Fatalf("SelectChildren: expected no results, got %+v", result)
	}
}

func TestNewSpaceRelationFromEvent(t *testing.T) {
	testCases := []struct {
		event        Event
		wantRelation *SpaceRelation
		wantDeleted  bool
	}{
		// child: no suggested or ordering
		{
			event: Event{
				Type:     "m.space.child",
				StateKey: "!child",
				RoomID:   "!parent",
				JSON:     json.RawMessage(`{"type":"m.space.child","state_key":"!child","room_id":"!parent","content":{"via":["example.com"]}}`),
			},
			wantDeleted: false,
			wantRelation: &SpaceRelation{
				Parent:   "!parent",
				Child:    "!child",
				Relation: RelationMSpaceChild,
			},
		},
		// child: with suggested and ordering
		{
			event: Event{
				Type:     "m.space.child",
				StateKey: "!child",
				RoomID:   "!parent",
				JSON:     json.RawMessage(`{"type":"m.space.child","state_key":"!child","room_id":"!parent","content":{"via":["example.com"],"ordering":"abc","suggested":true}}`),
			},
			wantDeleted: false,
			wantRelation: &SpaceRelation{
				Parent:      "!parent",
				Child:       "!child",
				Relation:    RelationMSpaceChild,
				Ordering:    "abc",
				IsSuggested: true,
			},
		},
		// child: redacted
		{
			event: Event{
				Type:     "m.space.child",
				StateKey: "!child",
				RoomID:   "!parent",
				JSON:     json.RawMessage(`{"type":"m.space.child","state_key":"!child","room_id":"!parent","content":{}}`),
			},
			wantDeleted: true,
			wantRelation: &SpaceRelation{
				Parent:   "!parent",
				Child:    "!child",
				Relation: RelationMSpaceChild,
			},
		},
		// child: malformed
		{
			event: Event{
				Type:     "m.space.child",
				StateKey: "!child",
				RoomID:   "!parent",
				JSON:     json.RawMessage(`{"type":"m.space.child","state_key":"!child","room_id":"!parent","content":{"via":"thisisnotanarray"}}`),
			},
			wantDeleted: true,
			wantRelation: &SpaceRelation{
				Parent:   "!parent",
				Child:    "!child",
				Relation: RelationMSpaceChild,
			},
		},
		// child: not a state event
		{
			event: Event{
				Type:   "m.space.child",
				RoomID: "!parent",
				JSON:   json.RawMessage(`{"type":"m.space.child","room_id":"!parent","content":{"via":["example.com"]}}`),
			},
			wantDeleted:  false,
			wantRelation: nil,
		},
		// parent
		{
			event: Event{
				Type:     "m.space.parent",
				StateKey: "!parent",
				RoomID:   "!child",
				JSON:     json.RawMessage(`{"type":"m.space.parent","state_key":"!parent","room_id":"!parent","content":{"via":["example.com"]}}`),
			},
			wantDeleted: false,
			wantRelation: &SpaceRelation{
				Parent:   "!parent",
				Child:    "!child",
				Relation: RelationMSpaceParent,
			},
		},
	}
	for _, tc := range testCases {
		gotRelation, gotDeleted := NewSpaceRelationFromEvent(tc.event)
		if gotDeleted != tc.wantDeleted {
			t.Errorf("%+v got deleted %v want %v", tc.event, gotDeleted, tc.wantDeleted)
		}
		if tc.wantRelation == nil && gotRelation != nil {
			t.Errorf("%+v got relation %+v want nil", tc.event, gotRelation)
		} else if tc.wantRelation != nil && gotRelation == nil {
			t.Errorf("%+v got nil relation, want %+v", tc.event, tc.wantRelation)
		}
		if gotRelation != nil && tc.wantRelation != nil {
			if !reflect.DeepEqual(*gotRelation, *tc.wantRelation) {
				t.Errorf("%+v got relation %+v want %+v", tc.event, gotRelation, tc.wantRelation)
			}
		}
	}
}

func TestHandleSpaceUpdates(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Rollback()
	table := NewSpacesTable(db)

	parentRoomID := "!parent"
	parentRoomID2 := "!parent2"
	childRoomID1 := "!child1"
	childRoomID2 := "!child2"

	// inject a space with 2 children
	e1 := Event{
		JSON: serialise(t, map[string]interface{}{
			"event_id":  "$1",
			"type":      "m.space.child",
			"room_id":   parentRoomID,
			"state_key": childRoomID1,
			"content": map[string]interface{}{
				"ordering": "abc",
				"via":      []string{"example.com"},
			},
		}),
	}
	noError(t, e1.ensureFieldsSetOnEvent())
	e2 := Event{
		JSON: serialise(t, map[string]interface{}{
			"event_id":  "$2",
			"type":      "m.space.child",
			"room_id":   parentRoomID,
			"state_key": childRoomID2,
			"content": map[string]interface{}{
				"via":       []string{"example.com"},
				"suggested": true,
			},
		}),
	}
	noError(t, e2.ensureFieldsSetOnEvent())

	noError(t, table.HandleSpaceUpdates(txn, []Event{e1, e2}))
	result, err := table.SelectChildren(txn, []string{parentRoomID})
	noError(t, err)
	matchAnyOrder(t, result[parentRoomID], []SpaceRelation{{
		Parent:   parentRoomID,
		Child:    childRoomID1,
		Relation: RelationMSpaceChild,
		Ordering: "abc",
	}, {
		Parent:      parentRoomID,
		Child:       childRoomID2,
		Relation:    RelationMSpaceChild,
		IsSuggested: true,
	}})

	// delete a link
	e3 := Event{
		JSON: serialise(t, map[string]interface{}{
			"event_id":  "$3",
			"type":      "m.space.child",
			"room_id":   parentRoomID,
			"state_key": childRoomID2,
			"content":   map[string]interface{}{},
		}),
	}
	noError(t, e3.ensureFieldsSetOnEvent())
	noError(t, table.HandleSpaceUpdates(txn, []Event{e3}))
	result, err = table.SelectChildren(txn, []string{parentRoomID})
	noError(t, err)
	matchAnyOrder(t, result[parentRoomID], []SpaceRelation{{
		Parent:   parentRoomID,
		Child:    childRoomID1,
		Relation: RelationMSpaceChild,
		Ordering: "abc",
	}})

	// add then delete then add a link - should end up with it being added as that was the last op
	e4 := Event{
		JSON: serialise(t, map[string]interface{}{
			"event_id":  "$4",
			"type":      "m.space.child",
			"room_id":   parentRoomID,
			"state_key": childRoomID2,
			"content": map[string]interface{}{
				"via": []string{"example.com"},
			},
		}),
	}
	noError(t, e4.ensureFieldsSetOnEvent())
	e5 := Event{
		JSON: serialise(t, map[string]interface{}{
			"event_id":  "$5",
			"type":      "m.space.child",
			"room_id":   parentRoomID,
			"state_key": childRoomID2,
			"content":   map[string]interface{}{},
		}),
	}
	noError(t, e5.ensureFieldsSetOnEvent())
	e6 := Event{
		JSON: serialise(t, map[string]interface{}{
			"event_id":  "$6",
			"type":      "m.space.child",
			"room_id":   parentRoomID,
			"state_key": childRoomID2,
			"content": map[string]interface{}{
				"via":      []string{"example.com"},
				"ordering": "123",
			},
		}),
	}
	noError(t, e6.ensureFieldsSetOnEvent())
	noError(t, table.HandleSpaceUpdates(txn, []Event{e4, e5, e6}))
	result, err = table.SelectChildren(txn, []string{parentRoomID})
	noError(t, err)
	matchAnyOrder(t, result[parentRoomID], []SpaceRelation{{
		Parent:   parentRoomID,
		Child:    childRoomID1,
		Relation: RelationMSpaceChild,
		Ordering: "abc",
	}, {
		Parent:   parentRoomID,
		Child:    childRoomID2,
		Relation: RelationMSpaceChild,
		Ordering: "123",
	}})

	// check parent links work
	e7 := Event{
		JSON: serialise(t, map[string]interface{}{
			"event_id":  "$7",
			"type":      "m.space.parent",
			"room_id":   childRoomID2,
			"state_key": parentRoomID,
			"content": map[string]interface{}{
				"via": []string{"example.com"},
			},
		}),
	}
	noError(t, e7.ensureFieldsSetOnEvent())
	noError(t, table.HandleSpaceUpdates(txn, []Event{e7}))
	result, err = table.SelectChildren(txn, []string{parentRoomID})
	noError(t, err)
	matchAnyOrder(t, result[parentRoomID], []SpaceRelation{{
		Parent:   parentRoomID,
		Child:    childRoomID1,
		Relation: RelationMSpaceChild,
		Ordering: "abc",
	}, {
		Parent:   parentRoomID,
		Child:    childRoomID2,
		Relation: RelationMSpaceChild,
		Ordering: "123",
	}, {
		Parent:   parentRoomID,
		Child:    childRoomID2,
		Relation: RelationMSpaceParent,
	}})

	// can upsert ordering value
	e8 := Event{
		JSON: serialise(t, map[string]interface{}{
			"event_id":  "$8",
			"type":      "m.space.child",
			"room_id":   parentRoomID,
			"state_key": childRoomID2,
			"content": map[string]interface{}{
				"via":      []string{"example.com"},
				"ordering": "qwerty",
			},
		}),
	}
	noError(t, e8.ensureFieldsSetOnEvent())
	noError(t, table.HandleSpaceUpdates(txn, []Event{e8}))
	result, err = table.SelectChildren(txn, []string{parentRoomID})
	noError(t, err)
	matchAnyOrder(t, result[parentRoomID], []SpaceRelation{{
		Parent:   parentRoomID,
		Child:    childRoomID1,
		Relation: RelationMSpaceChild,
		Ordering: "abc",
	}, {
		Parent:   parentRoomID,
		Child:    childRoomID2,
		Relation: RelationMSpaceChild,
		Ordering: "qwerty",
	}, {
		Parent:   parentRoomID,
		Child:    childRoomID2,
		Relation: RelationMSpaceParent,
	}})

	// can get multiple spaces
	e9 := Event{
		JSON: serialise(t, map[string]interface{}{
			"event_id":  "$9",
			"type":      "m.space.child",
			"room_id":   parentRoomID2,
			"state_key": childRoomID2,
			"content": map[string]interface{}{
				"via":      []string{"example.com"},
				"ordering": "qwerty2",
			},
		}),
	}
	noError(t, e9.ensureFieldsSetOnEvent())
	noError(t, table.HandleSpaceUpdates(txn, []Event{e9}))
	result, err = table.SelectChildren(txn, []string{parentRoomID, parentRoomID2})
	noError(t, err)
	matchAnyOrder(t, result[parentRoomID], []SpaceRelation{{
		Parent:   parentRoomID,
		Child:    childRoomID1,
		Relation: RelationMSpaceChild,
		Ordering: "abc",
	}, {
		Parent:   parentRoomID,
		Child:    childRoomID2,
		Relation: RelationMSpaceChild,
		Ordering: "qwerty",
	}, {
		Parent:   parentRoomID,
		Child:    childRoomID2,
		Relation: RelationMSpaceParent,
	}})
	matchAnyOrder(t, result[parentRoomID2], []SpaceRelation{{
		Parent:   parentRoomID2,
		Child:    childRoomID2,
		Relation: RelationMSpaceChild,
		Ordering: "qwerty2",
	}})

}

func serialise(t *testing.T, j map[string]interface{}) json.RawMessage {
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("serialise: %s", err)
	}
	return json.RawMessage(b)
}
