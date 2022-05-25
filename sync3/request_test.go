package sync3

import (
	"reflect"
	"testing"
)

func TestRoomSubscriptionUnion(t *testing.T) {
	testCases := []struct {
		name              string
		a                 RoomSubscription
		b                 *RoomSubscription
		wantQueryStateMap map[string][]string
		matches           [][2]string
		noMatches         [][2]string
	}{
		{
			name:              "single event",
			a:                 RoomSubscription{RequiredState: [][2]string{{"m.room.name", ""}}},
			wantQueryStateMap: map[string][]string{"m.room.name": {""}},
			matches:           [][2]string{{"m.room.name", ""}},
			noMatches:         [][2]string{{"m.room.name2", ""}, {"m.room.name2", "2"}, {"m.room.name", "2"}},
		},
		{
			name: "two disjoint events",
			a:    RoomSubscription{RequiredState: [][2]string{{"m.room.name", ""}, {"m.room.topic", ""}}},
			wantQueryStateMap: map[string][]string{
				"m.room.name":  {""},
				"m.room.topic": {""},
			},
			matches: [][2]string{{"m.room.name", ""}, {"m.room.topic", ""}},
			noMatches: [][2]string{
				{"m.room.name2", ""}, {"m.room.name2", "2"}, {"m.room.name", "2"},
				{"m.room.topic2", ""}, {"m.room.topic2", "2"}, {"m.room.topic", "2"},
			},
		},
		{
			name: "single type, multiple state keys",
			a:    RoomSubscription{RequiredState: [][2]string{{"m.room.name", ""}, {"m.room.name", "foo"}}},
			wantQueryStateMap: map[string][]string{
				"m.room.name": {"", "foo"},
			},
			matches: [][2]string{{"m.room.name", ""}, {"m.room.name", "foo"}},
			noMatches: [][2]string{
				{"m.room.name2", "foo"}, {"m.room.name2", ""}, {"m.room.name", "2"},
			},
		},
		{
			name: "single type, multiple state keys UNION",
			a:    RoomSubscription{RequiredState: [][2]string{{"m.room.name", ""}}},
			b:    &RoomSubscription{RequiredState: [][2]string{{"m.room.name", "foo"}}},
			wantQueryStateMap: map[string][]string{
				"m.room.name": {"", "foo"},
			},
			matches: [][2]string{{"m.room.name", ""}, {"m.room.name", "foo"}},
			noMatches: [][2]string{
				{"m.room.name2", "foo"}, {"m.room.name2", ""}, {"m.room.name", "2"},
			},
		},
		{
			name:              "all events *,*",
			a:                 RoomSubscription{RequiredState: [][2]string{{"*", "*"}}},
			wantQueryStateMap: make(map[string][]string),
			matches:           [][2]string{{"m.room.name", ""}, {"m.room.name", "foo"}},
		},
		{
			name:              "all events *,* with other event",
			a:                 RoomSubscription{RequiredState: [][2]string{{"*", "*"}, {"m.room.name", ""}}},
			wantQueryStateMap: make(map[string][]string),
			matches:           [][2]string{{"m.room.name", ""}, {"m.room.name", "foo"}, {"a", "b"}},
		},
		{
			name:              "all events *,* with other event UNION",
			a:                 RoomSubscription{RequiredState: [][2]string{{"m.room.name", ""}}},
			b:                 &RoomSubscription{RequiredState: [][2]string{{"*", "*"}}},
			wantQueryStateMap: make(map[string][]string),
			matches:           [][2]string{{"m.room.name", ""}, {"m.room.name", "foo"}, {"a", "b"}},
		},
		{
			name: "wildcard state keys with explicit state keys",
			a:    RoomSubscription{RequiredState: [][2]string{{"m.room.name", "*"}, {"m.room.name", ""}}},
			wantQueryStateMap: map[string][]string{
				"m.room.name": nil,
			},
			matches:   [][2]string{{"m.room.name", ""}, {"m.room.name", "foo"}},
			noMatches: [][2]string{{"m.room.name2", ""}, {"foo", "bar"}},
		},
		{
			name:              "wildcard state keys with wildcard event types",
			a:                 RoomSubscription{RequiredState: [][2]string{{"m.room.name", "*"}, {"*", "foo"}}},
			wantQueryStateMap: make(map[string][]string),
			matches: [][2]string{
				{"m.room.name", ""}, {"m.room.name", "foo"}, {"name", "foo"},
			},
			noMatches: [][2]string{
				{"m.room.name2", ""}, {"foo", "bar"},
			},
		},
		{
			name:              "wildcard state keys with wildcard event types UNION",
			a:                 RoomSubscription{RequiredState: [][2]string{{"m.room.name", "*"}}},
			b:                 &RoomSubscription{RequiredState: [][2]string{{"*", "foo"}}},
			wantQueryStateMap: make(map[string][]string),
			matches: [][2]string{
				{"m.room.name", ""}, {"m.room.name", "foo"}, {"name", "foo"},
			},
			noMatches: [][2]string{
				{"m.room.name2", ""}, {"foo", "bar"},
			},
		},
		{
			name:              "wildcard event types with explicit state keys",
			a:                 RoomSubscription{RequiredState: [][2]string{{"*", "foo"}, {"*", "bar"}, {"m.room.name", ""}}},
			wantQueryStateMap: make(map[string][]string),
			matches:           [][2]string{{"m.room.name", ""}, {"m.room.name", "foo"}, {"name", "foo"}, {"name", "bar"}},
			noMatches:         [][2]string{{"name", "baz"}, {"name", ""}},
		},
	}
	for _, tc := range testCases {
		sub := tc.a
		if tc.b != nil {
			sub = tc.a.Combine(*tc.b)
		}
		rsm := sub.RequiredStateMap()
		got := rsm.QueryStateMap()
		if !reflect.DeepEqual(got, tc.wantQueryStateMap) {
			t.Errorf("%s: got query state map %+v want %+v", tc.name, got, tc.wantQueryStateMap)
		}
		if tc.matches != nil {
			for _, match := range tc.matches {
				if !rsm.Include(match[0], match[1]) {
					t.Errorf("%s: want '%s' %s' to match but it didn't", tc.name, match[0], match[1])
				}
			}
			for _, noMatch := range tc.noMatches {
				if rsm.Include(noMatch[0], noMatch[1]) {
					t.Errorf("%s: want '%s' %s' to NOT match but it did", tc.name, noMatch[0], noMatch[1])
				}
			}
		}
	}
}

func TestRequestApplyDeltas(t *testing.T) {
	testCases := []struct {
		input Request
		tests []struct {
			next  Request
			check func(t *testing.T, r Request, subs, unsubs []string)
		}
	}{
		{
			input: Request{
				Lists: []RequestList{
					{
						Sort: []string{SortByName},
						RoomSubscription: RoomSubscription{
							TimelineLimit: 5,
						},
					},
				},
				RoomSubscriptions: map[string]RoomSubscription{
					"!foo:bar": {
						TimelineLimit: 10,
					},
				},
			},
			tests: []struct {
				next  Request
				check func(t *testing.T, r Request, subs, unsubs []string)
			}{
				// check overwriting of sort and updating subs without adding new ones
				{
					next: Request{
						Lists: []RequestList{
							{
								Sort: []string{SortByRecency},
							},
						},
						RoomSubscriptions: map[string]RoomSubscription{
							"!foo:bar": {
								TimelineLimit: 100,
							},
						},
					},
					check: func(t *testing.T, r Request, subs, unsubs []string) {
						ensureEmpty(t, subs, unsubs)
						if r.RoomSubscriptions["!foo:bar"].TimelineLimit != 100 {
							t.Errorf("subscription was not updated, got %+v", r)
						}
					},
				},
				// check adding a subs
				{
					next: Request{
						Lists: []RequestList{
							{
								Sort: []string{SortByRecency},
							},
						},
						RoomSubscriptions: map[string]RoomSubscription{
							"!bar:baz": {
								TimelineLimit: 42,
							},
						},
					},
					check: func(t *testing.T, r Request, subs, unsubs []string) {
						ensureEmpty(t, unsubs)
						if r.RoomSubscriptions["!bar:baz"].TimelineLimit != 42 {
							t.Errorf("subscription was not added, got %+v", r)
						}
						if !reflect.DeepEqual(subs, []string{"!bar:baz"}) {
							t.Errorf("subscription not added: got %v", subs)
						}
					},
				},
				// check unsubscribing
				{
					next: Request{
						Lists: []RequestList{
							{
								Sort: []string{SortByRecency},
							},
						},
						UnsubscribeRooms: []string{"!foo:bar"},
					},
					check: func(t *testing.T, r Request, subs, unsubs []string) {
						ensureEmpty(t, subs)
						if len(r.RoomSubscriptions) != 0 {
							t.Errorf("Expected empty subs, got %+v", r.RoomSubscriptions)
						}
						if !reflect.DeepEqual(unsubs, []string{"!foo:bar"}) {
							t.Errorf("subscription not removed: got %v", unsubs)
						}
					},
				},
				// check subscribing and unsubscribing = no change
				{
					next: Request{
						Lists: []RequestList{
							{
								Sort: []string{SortByRecency},
							},
						},
						RoomSubscriptions: map[string]RoomSubscription{
							"!bar:baz": {
								TimelineLimit: 42,
							},
						},
						UnsubscribeRooms: []string{"!bar:baz"},
					},
					check: func(t *testing.T, r Request, subs, unsubs []string) {
						ensureEmpty(t, subs, unsubs)
						if len(r.RoomSubscriptions) != 1 {
							t.Errorf("Expected 1 subs, got %+v", r.RoomSubscriptions)
						}
					},
				},
			},
		},
	}
	for _, tc := range testCases {
		for _, test := range tc.tests {
			result, subs, unsubs := tc.input.ApplyDelta(&test.next)
			test.check(t, *result, subs, unsubs)
		}
	}
}

func ensureEmpty(t *testing.T, others ...[]string) {
	t.Helper()
	for _, slice := range others {
		if len(slice) != 0 {
			t.Fatalf("got %v - want nothing", slice)
		}
	}
}
