package syncv3_test

import (
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// Make this graph:
//
//	   A       D      <-- parents
//	.--`--.    |
//	B     C    E   F  <-- children
//
// and query:
//
//	spaces[A] => B,C
//	spaces[D] => E
//	spaces[A,B] => B,C,E
func TestSpacesFilter(t *testing.T) {
	alice := registerNewUser(t)
	parentA := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"creation_content": map[string]string{
			"type": "m.space",
		},
	})
	parentD := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"creation_content": map[string]string{
			"type": "m.space",
		},
	})
	roomB := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	roomC := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	roomE := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	roomF := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	t.Logf("A: %s B: %s C: %s D: %s E: %s F: %s", parentA, roomB, roomC, parentD, roomE, roomF)
	alice.SendEventSynced(t, parentA, Event{
		Type:     "m.space.child",
		StateKey: &roomB,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})
	alice.SendEventSynced(t, parentA, Event{
		Type:     "m.space.child",
		StateKey: &roomC,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})
	alice.SendEventSynced(t, parentD, Event{
		Type:     "m.space.child",
		StateKey: &roomE,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})
	time.Sleep(100 * time.Millisecond) // let the proxy process this

	doSpacesListRequest := func(spaces []string, pos *string, listMatchers ...m.ListMatcher) *sync3.Response {
		t.Helper()
		var opts []RequestOpt
		if pos != nil {
			opts = append(opts, WithPos(*pos))
		}
		t.Logf("requesting rooms in spaces %v", spaces)
		res := alice.SlidingSync(t, sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Ranges: [][2]int64{{0, 20}},
					Filters: &sync3.RequestFilters{
						Spaces: spaces,
					},
				},
			},
		}, opts...)
		m.MatchResponse(t, res, m.MatchList("a", listMatchers...))
		return res
	}

	doInitialSpacesListRequest := func(spaces, wantRoomIDs []string) *sync3.Response {
		t.Helper()
		t.Logf("requesting initial rooms in spaces %v expecting %v", spaces, wantRoomIDs)
		return doSpacesListRequest(spaces, nil, m.MatchV3Count(len(wantRoomIDs)), m.MatchV3Ops(
			m.MatchV3SyncOp(
				0, int64(len(wantRoomIDs))-1, wantRoomIDs, true,
			),
		))
	}

	//  spaces[A] => B,C
	//  spaces[D] => E
	//  spaces[A,B] => B,C,E
	testCases := []struct {
		Spaces      []string
		WantRoomIDs []string
	}{
		{Spaces: []string{parentA}, WantRoomIDs: []string{roomB, roomC}},
		{Spaces: []string{parentD}, WantRoomIDs: []string{roomE}},
		{Spaces: []string{parentA, parentD}, WantRoomIDs: []string{roomB, roomC, roomE}},
	}
	for _, tc := range testCases {
		doInitialSpacesListRequest(tc.Spaces, tc.WantRoomIDs)
	}

	// now move F into D and re-query D
	alice.SendEventSynced(t, parentD, Event{
		Type:     "m.space.child",
		StateKey: &roomF,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})
	time.Sleep(100 * time.Millisecond) // let the proxy process this
	doInitialSpacesListRequest([]string{parentD}, []string{roomF, roomE})

	// now remove B and re-query A
	alice.SendEventSynced(t, parentA, Event{
		Type:     "m.space.child",
		StateKey: &roomB,
		Content:  map[string]interface{}{},
	})
	time.Sleep(100 * time.Millisecond) // let the proxy process this
	res := doInitialSpacesListRequest([]string{parentA}, []string{roomC})

	// now live stream an update to ensure it gets added
	alice.SendEventSynced(t, parentA, Event{
		Type:     "m.space.child",
		StateKey: &roomB,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})
	time.Sleep(100 * time.Millisecond) // let the proxy process this
	res = doSpacesListRequest([]string{parentA}, &res.Pos,
		m.MatchV3Count(2), m.MatchV3Ops(
			m.MatchV3DeleteOp(1),
			m.MatchV3InsertOp(1, roomB),
		),
	)

	// now completely change the space filter and ensure we see the right rooms
	doSpacesListRequest([]string{parentD}, &res.Pos,
		m.MatchV3Count(2), m.MatchV3Ops(
			m.MatchV3InvalidateOp(0, 1),
			m.MatchV3SyncOp(0, 1, []string{roomF, roomE}, true),
		),
	)
}
