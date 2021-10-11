package sync3

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/testutils"
)

func TestGlobalCacheLoadState(t *testing.T) {
	store := state.NewStorage(postgresConnectionString)
	roomID := "!TestConnMapLoadState:localhost"
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
	_, latest, err := store.Accumulate(roomID, events)
	if err != nil {
		t.Fatalf("Accumulate: %s", err)
	}
	globalCache := NewGlobalCache(store)
	testCases := []struct {
		requiredState [][2]string
		wantEvents    []json.RawMessage
	}{
		{ // single required state returns a single event
			requiredState: [][2]string{
				{"m.room.create", ""},
			},
			wantEvents: []json.RawMessage{events[0]},
		},
		{ // unknown event returns no error but no events either
			requiredState: [][2]string{
				{"i do not exist", ""},
			},
			wantEvents: []json.RawMessage{},
		},
		{ // multiple required state returns both
			requiredState: [][2]string{
				{"m.room.create", ""}, {"m.room.join_rules", ""},
			},
			wantEvents: []json.RawMessage{events[0], events[2]},
		},
		{ // mixed existing and non-existant state returns stuff that exists
			requiredState: [][2]string{
				{"i do not exist", ""}, {"m.room.create", ""},
			},
			wantEvents: []json.RawMessage{events[0]},
		},
		{ // state key matching works
			requiredState: [][2]string{
				{"m.room.member", bob},
			},
			wantEvents: []json.RawMessage{events[3]},
		},
		{ // wildcard matching works
			requiredState: [][2]string{
				{"m.room.member", "*"},
			},
			wantEvents: []json.RawMessage{events[1], events[3], events[4]},
		},
		{ // multiple entries for the same state do not return duplicates
			requiredState: [][2]string{
				{"m.room.create", ""}, {"m.room.create", ""}, {"m.room.create", ""}, {"m.room.create", ""},
			},
			wantEvents: []json.RawMessage{events[0]},
		},
		{ // the newest event is returned in a match
			requiredState: [][2]string{
				{"m.room.name", ""},
			},
			wantEvents: []json.RawMessage{events[6]},
		},
	}
	for _, tc := range testCases {
		got := globalCache.LoadRoomState(roomID, latest, tc.requiredState)
		if len(got) != len(tc.wantEvents) {
			t.Errorf("LoadState for input %v got %d events want %d", tc.requiredState, len(got), len(tc.wantEvents))
			continue
		}
		for i := range tc.wantEvents {
			if !bytes.Equal(got[i], tc.wantEvents[i]) {
				t.Errorf("LoadState for input %v at pos %d:\ngot  %s\nwant %s", tc.requiredState, i, string(got[i]), string(tc.wantEvents[i]))
			}
		}
	}
}
