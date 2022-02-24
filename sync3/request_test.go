package sync3

import (
	"reflect"
	"testing"
)

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
