package streams

import (
	"reflect"
	"testing"
)

func TestRequestApplyDeltas(t *testing.T) {
	testCases := []struct {
		original    Request
		delta       Request
		combined    Request
		deltasExist bool
	}{
		// test combining two separate streams
		{
			original: Request{
				Typing: &FilterTyping{
					RoomID: "foo",
				},
			},
			delta: Request{
				ToDevice: &FilterToDevice{
					Limit: 100,
				},
			},
			combined: Request{
				Typing: &FilterTyping{
					RoomID: "foo",
				},
				ToDevice: &FilterToDevice{
					Limit: 100,
				},
			},
			deltasExist: true,
		},
		// test updating params on the same stream
		{
			original: Request{
				Typing: &FilterTyping{
					RoomID: "foo",
				},
			},
			delta: Request{
				Typing: &FilterTyping{
					RoomID: "bar",
				},
			},
			combined: Request{
				Typing: &FilterTyping{
					RoomID: "bar",
				},
			},
			deltasExist: true,
		},
		// test no-op same filter
		{
			original: Request{
				Typing: &FilterTyping{
					RoomID: "foo",
				},
			},
			delta: Request{
				Typing: &FilterTyping{
					RoomID: "foo",
				},
			},
			combined: Request{
				Typing: &FilterTyping{
					RoomID: "foo",
				},
			},
			deltasExist: false,
		},
		// test deleting params on the same stream
		// NB: this means to delete a stream you need to do { typing: {} } as omission of `typing` implies "continue as you are"
		{
			original: Request{
				Typing: &FilterTyping{
					RoomID: "foo",
				},
			},
			delta: Request{
				Typing: &FilterTyping{},
			},
			combined: Request{
				Typing: &FilterTyping{},
			},
			deltasExist: true,
		},
	}

	for _, tc := range testCases {
		// copy the original as ApplyDeltas side-effects by modifying the fields
		req := tc.original
		gotDeltasExist, err := req.ApplyDeltas(&tc.delta)
		if err != nil {
			t.Fatalf("ApplyDeltas: %+v returned error: %s", tc, err)
		}
		if gotDeltasExist != tc.deltasExist {
			t.Errorf("ApplyDeltas: %+v returned deltas exist: %v want %v", tc, gotDeltasExist, tc.deltasExist)
		}
		if !reflect.DeepEqual(req, tc.combined) {
			t.Errorf("applying %+v returned incorrect combination, got %+v", tc, req)
		}
	}
}
