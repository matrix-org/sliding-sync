package syncv3_test

import (
	"testing"

	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils/m"
)

// Make this graph:
//
//      A       D      <-- parents
//   .--`--.    |
//   B     C    E   F  <-- children
//
// and query:
//  spaces[A] => B,C
//  spaces[D] => E
//  spaces[A,B] => B,C,E
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
		"type":   "m.space",
	})
	roomC := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"type":   "m.space",
	})
	roomE := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"type":   "m.space",
	})
	roomF := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"type":   "m.space",
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
		res := alice.SlidingSync(t, sync3.Request{
			Lists: []sync3.RequestList{
				{
					Ranges: [][2]int64{{0, 20}},
					Filters: &sync3.RequestFilters{
						Spaces: tc.Spaces,
					},
				},
			},
		})
		m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(len(tc.WantRoomIDs)), m.MatchV3Ops(
			m.MatchV3SyncOp(
				0, 20, tc.WantRoomIDs, true,
			),
		)))
	}

	// now move F into D and re-query D
	alice.SendEventSynced(t, parentD, Event{
		Type:     "m.space.child",
		StateKey: &roomF,
		Content: map[string]interface{}{
			"via": []string{"example.com"},
		},
	})
	res := alice.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: [][2]int64{{0, 20}},
				Filters: &sync3.RequestFilters{
					Spaces: []string{parentD},
				},
			},
		},
	})
	wantRooms := []string{roomF, roomE}
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(len(wantRooms)), m.MatchV3Ops(
		m.MatchV3SyncOp(
			0, 20, wantRooms, true,
		),
	)))

	// now remove B and re-query A
	alice.SendEventSynced(t, parentA, Event{
		Type:     "m.space.child",
		StateKey: &roomB,
		Content:  map[string]interface{}{},
	})
	res = alice.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: [][2]int64{{0, 20}},
				Filters: &sync3.RequestFilters{
					Spaces: []string{parentA},
				},
			},
		},
	})
	wantRooms = []string{roomC}
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(len(wantRooms)), m.MatchV3Ops(
		m.MatchV3SyncOp(
			0, 20, wantRooms, true,
		),
	)))

}
