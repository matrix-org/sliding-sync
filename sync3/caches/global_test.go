package caches_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/matrix-org/sliding-sync/testutils"
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
		testutils.NewJoinEvent(t, alice),
		testutils.NewStateEvent(t, "m.room.join_rules", "", alice, map[string]interface{}{"join_rule": "public"}),
		testutils.NewJoinEvent(t, bob),
		testutils.NewJoinEvent(t, charlie),
		testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "The Room Name"}),
		testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "The Updated Room Name"}),
	}
	eventsRoom2 := []json.RawMessage{
		testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
		testutils.NewJoinEvent(t, alice),
		testutils.NewStateEvent(t, "m.room.join_rules", "", alice, map[string]interface{}{"join_rule": "public"}),
		testutils.NewJoinEvent(t, bob),
		testutils.NewJoinEvent(t, charlie),
		testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "The Room Name"}),
		testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "The Updated Room Name"}),
	}
	_, _, err := store.Accumulate(roomID2, "", eventsRoom2)
	if err != nil {
		t.Fatalf("Accumulate: %s", err)
	}

	_, latestNIDs, err := store.Accumulate(roomID, "", events)
	if err != nil {
		t.Fatalf("Accumulate: %s", err)
	}
	latest := latestNIDs[len(latestNIDs)-1]
	globalCache := caches.NewGlobalCache(store)
	testCases := []struct {
		name                  string
		me                    string
		requiredState         [][2]string
		wantEvents            map[string][]json.RawMessage
		roomToUsersInTimeline map[string][]string
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
				roomID2: {eventsRoom2[3]},
			},
		},
		{
			name: "using $ME works",
			me:   alice,
			requiredState: [][2]string{
				{"m.room.member", sync3.StateKeyMe},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID:  {events[1]},
				roomID2: {eventsRoom2[1]},
			},
		},
		{
			name: "using $ME ignores other member events",
			me:   "@bogus-user:example.com",
			requiredState: [][2]string{
				{"m.room.member", sync3.StateKeyMe},
			},
			wantEvents: nil,
		},
		{
			name: "using $LAZY works",
			me:   alice,
			requiredState: [][2]string{
				{"m.room.member", sync3.StateKeyLazy},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID:  {events[1], events[4]},
				roomID2: {eventsRoom2[1], eventsRoom2[4]},
			},
			roomToUsersInTimeline: map[string][]string{
				roomID:  {alice, charlie},
				roomID2: {alice, charlie},
			},
		},
		{
			name: "using $LAZY and $ME works",
			me:   alice,
			requiredState: [][2]string{
				{"m.room.member", sync3.StateKeyLazy},
				{"m.room.member", sync3.StateKeyMe},
			},
			wantEvents: map[string][]json.RawMessage{
				roomID:  {events[1], events[4]},
				roomID2: {eventsRoom2[1], eventsRoom2[4]},
			},
			roomToUsersInTimeline: map[string][]string{
				roomID:  {charlie},
				roomID2: {charlie},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var roomIDs []string
			for r := range tc.wantEvents {
				roomIDs = append(roomIDs, r)
			}
			rs := sync3.RoomSubscription{
				RequiredState: tc.requiredState,
			}
			gotMap, _ := globalCache.LoadRoomState(ctx, roomIDs, latest, rs.RequiredStateMap(tc.me), tc.roomToUsersInTimeline)
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
