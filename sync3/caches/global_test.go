package caches

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/testutils"
)

func TestGlobalCacheLoadState(t *testing.T) {
	ctx := context.Background()
	store := state.NewStorage(postgresConnectionString)
	roomID := "!TestConnMapLoadState:localhost"
	roomID2 := "!another:localhost"
	alice := "@alice:localhost"
	bob := "@bob:localhost"
	charlie := "@charlie:localhost"
	events := []json.RawMessage{
		testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
		testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
		testutils.NewStateEvent(t, "m.room.join_rules", "", alice, map[string]interface{}{"join_rule": "public"}),
		testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
		testutils.NewStateEvent(t, "m.room.member", charlie, charlie, map[string]interface{}{"membership": "join"}),
		testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "The Room Name"}),
		testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "The Updated Room Name"}),
	}
	moreEvents := []json.RawMessage{
		testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
		testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
		testutils.NewStateEvent(t, "m.room.join_rules", "", alice, map[string]interface{}{"join_rule": "public"}),
		testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
		testutils.NewStateEvent(t, "m.room.member", charlie, charlie, map[string]interface{}{"membership": "join"}),
		testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "The Room Name"}),
		testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "The Updated Room Name"}),
	}
	_, _, err := store.Accumulate(roomID2, "", moreEvents)
	if err != nil {
		t.Fatalf("Accumulate: %s", err)
	}

	_, latest, err := store.Accumulate(roomID, "", events)
	if err != nil {
		t.Fatalf("Accumulate: %s", err)
	}
	globalCache := NewGlobalCache(store)
	testCases := []struct {
		name          string
		requiredState [][2]string
		wantEvents    map[string][]json.RawMessage
	}{
		{
			name: "single required state returns a single event",
			requiredState: [][2]string{
				{"m.room.create", ""},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID: {events[0]},
			},
		},
		{
			name: "unknown event returns no error but no events either",
			requiredState: [][2]string{
				{"i do not exist", ""},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID: {},
			},
		},
		{
			name: "multiple required state returns both",
			requiredState: [][2]string{
				{"m.room.create", ""}, {"m.room.join_rules", ""},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID: {events[0], events[2]},
			},
		},
		{
			name: "mixed existing and non-existant state returns stuff that exists",
			requiredState: [][2]string{
				{"i do not exist", ""}, {"m.room.create", ""},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID: {events[0]},
			},
		},
		{
			name: "state key matching works",
			requiredState: [][2]string{
				{"m.room.member", bob},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID: {events[3]},
			},
		},
		{
			name: "state key wildcard matching works",
			requiredState: [][2]string{
				{"m.room.member", "*"},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID: {events[1], events[3], events[4]},
			},
		},
		{
			name: "event type wildcard matching works",
			requiredState: [][2]string{
				{"*", ""},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID: {events[0], events[2], events[6]},
			},
		},
		{
			name: "all state wildcard matching works",
			requiredState: [][2]string{
				{"*", "*"},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID: {events[0], events[1], events[2], events[3], events[4], events[6]},
			},
		},
		{
			name: "multiple entries for the same state do not return duplicates",
			requiredState: [][2]string{
				{"m.room.create", ""}, {"m.room.create", ""}, {"m.room.create", ""}, {"m.room.create", ""},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID: {events[0]},
			},
		},
		{
			name: "the newest event is returned in a match",
			requiredState: [][2]string{
				{"m.room.name", ""},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID: {events[6]},
			},
		},
		{
			name: "state key matching for multiple rooms works",
			requiredState: [][2]string{
				{"m.room.member", bob},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID:  {events[3]},
				roomID2: {moreEvents[3]},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var roomIDs []string
			for r := range tc.wantEvents {
				roomIDs = append(roomIDs, r)
			}
			gotMap := globalCache.LoadRoomState(ctx, roomIDs, latest, tc.requiredState)
			for _, roomID := range roomIDs {
				got := gotMap[roomID]
				wantEvents := tc.wantEvents[roomID]
				if len(got) != len(wantEvents) {
					t.Errorf("LoadState %d for rooms %v required_state %v got %d events want %d.", latest, roomIDs, tc.requiredState, len(got), len(wantEvents))
					continue
				}
				for i := range wantEvents {
					if !bytes.Equal(got[i], wantEvents[i]) {
						t.Errorf("LoadState %d for input %v at pos %d:\ngot  %s\nwant %s", latest, tc.requiredState, i, string(got[i]), string(wantEvents[i]))
					}
				}
			}
		})
	}
}
