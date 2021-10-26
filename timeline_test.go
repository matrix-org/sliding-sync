package syncv3

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
)

// Inject 20 rooms with A,B,C as the most recent events. Then do a v3 request [0,3] with a timeline limit of 3
// and make sure we get scrolback for the 4 rooms we care about. Then, restart the server (so it repopulates caches)
// and attempt the same request again, making sure we get the same results.
func TestTimelinesOverRestarts(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString("syncv3_test_sync3_integration_timeline")
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	alice := "@TestTimelinesOverRestarts_alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN"
	// make 20 rooms, last room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 20)
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(time.Duration(i) * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!TestTimelines_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, ts.Add(3*time.Second)),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, ts.Add(4*time.Second)),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, ts.Add(5*time.Second)),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, ts.Add(6*time.Second)),
			}...),
		}
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	numTimelineEventsPerRoom := 3
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Rooms: sync3.SliceRanges{
			[2]int64{0, 3}, // first 4 rooms
		},
		TimelineLimit: int64(numTimelineEventsPerRoom),
	})
	// most recent 4 rooms
	var wantRooms []roomEvents
	i := 0
	for len(wantRooms) < 4 {
		wantRooms = append(wantRooms, allRooms[len(allRooms)-i-1])
		i++
	}
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != len(wantRooms) {
				return fmt.Errorf("want %d rooms, got %d", len(wantRooms), len(op.Rooms))
			}
			for i := range wantRooms {
				err := wantRooms[i].MatchRoom(
					op.Rooms[i],
					MatchRoomName(wantRooms[i].name),
					MatchRoomTimeline(wantRooms[i].events[len(wantRooms[i].events)-numTimelineEventsPerRoom:]),
				)
				if err != nil {
					return err
				}
			}
			return nil
		}),
	))

	// restart the server
	v3.restart(t, v2, pqString)

	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Rooms: sync3.SliceRanges{
			[2]int64{0, 3}, // first 4 rooms
		},
		TimelineLimit: int64(numTimelineEventsPerRoom),
	})
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != len(wantRooms) {
				return fmt.Errorf("want %d rooms, got %d", len(wantRooms), len(op.Rooms))
			}
			for i := range wantRooms {
				err := wantRooms[i].MatchRoom(
					op.Rooms[i],
					MatchRoomName(wantRooms[i].name),
					MatchRoomTimeline(wantRooms[i].events[len(wantRooms[i].events)-numTimelineEventsPerRoom:]),
				)
				if err != nil {
					return err
				}
			}
			return nil
		}),
	))
}
