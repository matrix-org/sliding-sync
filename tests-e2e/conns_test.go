package syncv3_test

import (
	"fmt"
	"testing"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// Test that you can have multiple connections with the same device, and they work independently.
func TestMultipleConns(t *testing.T) {
	alice := registerNewUser(t)
	roomID := alice.CreateRoom(t, map[string]interface{}{})

	resA := alice.SlidingSync(t, sync3.Request{
		ConnID: "A",
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
	})
	resB := alice.SlidingSync(t, sync3.Request{
		ConnID: "B",
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 2,
			},
		},
	})
	resC := alice.SlidingSync(t, sync3.Request{
		ConnID: "C",
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 0,
			},
		},
	})

	eventID := alice.SendEventSynced(t, roomID, Event{
		Type:     "m.room.name",
		StateKey: ptr(""),
		Content:  map[string]interface{}{"name": "pub"},
	})

	// all 3 conns should see the name event
	testCases := []struct {
		ConnID   string
		Res      *sync3.Response
		WriteRes func(res *sync3.Response)
	}{
		{
			ConnID: "A", Res: resA, WriteRes: func(res *sync3.Response) { resA = res },
		},
		{
			ConnID: "B", Res: resB, WriteRes: func(res *sync3.Response) { resB = res },
		},
		{
			ConnID: "C", Res: resC, WriteRes: func(res *sync3.Response) { resC = res },
		},
	}

	for _, tc := range testCases {
		t.Logf("Syncing as %v", tc.ConnID)
		r := alice.SlidingSyncUntil(t, tc.Res.Pos, sync3.Request{ConnID: tc.ConnID}, func(r *sync3.Response) error {
			room, ok := r.Rooms[roomID]
			if !ok {
				return fmt.Errorf("no room %v", roomID)
			}
			return MatchRoomTimelineMostRecent(1, []Event{{ID: eventID}})(room)
		})
		tc.WriteRes(r)
	}

	// change the requests in different ways, ensure they still work, without expiring.

	// Conn A: unsub from the room
	resA = alice.SlidingSync(t, sync3.Request{ConnID: "A", UnsubscribeRooms: []string{roomID}}, WithPos(resA.Pos))
	m.MatchResponse(t, resA, m.MatchRoomSubscriptionsStrict(nil)) // no rooms

	// Conn B: ask for the create event
	resB = alice.SlidingSync(t, sync3.Request{ConnID: "B", RoomSubscriptions: map[string]sync3.RoomSubscription{
		roomID: {
			RequiredState: [][2]string{{"m.room.create", ""}},
		},
	}}, WithPos(resB.Pos))
	m.MatchResponse(t, resB, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {MatchRoomRequiredStateStrict([]Event{{Type: "m.room.create", StateKey: ptr("")}})},
	}))

	// Conn C: create a list
	resC = alice.SlidingSync(t, sync3.Request{ConnID: "C", Lists: map[string]sync3.RequestList{
		"list": {
			Ranges: sync3.SliceRanges{{0, 10}},
		},
	}}, WithPos(resC.Pos))
	m.MatchResponse(t, resC, m.MatchList("list", m.MatchV3Count(1), m.MatchV3Ops(m.MatchV3SyncOp(0, 0, []string{roomID}))))
}
