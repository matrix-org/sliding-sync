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
// and attempt the same request again, making sure we get the same results. Then add in some "live" v2 events
// and make sure the initial scrollback includes these new live events.
func TestTimelines(t *testing.T) {
	// setup code
	pqString := testutils.PrepareDBConnectionString("syncv3_test_sync3_integration_timeline")
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	alice := "@TestTimelines_alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN_TestTimelines"
	// make 20 rooms, last room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 20)
	latestTimestamp := time.Now()
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
		if ts.After(latestTimestamp) {
			latestTimestamp = ts
		}
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// most recent 4 rooms
	var wantRooms []roomEvents
	i := 0
	for len(wantRooms) < 4 {
		wantRooms = append(wantRooms, allRooms[len(allRooms)-i-1])
		i++
	}
	numTimelineEventsPerRoom := 3

	t.Run("timelines load initially", testTimelineLoadInitialEvents(v3, aliceToken, len(allRooms), wantRooms, numTimelineEventsPerRoom))
	// restart the server
	v3.restart(t, v2, pqString)
	t.Run("timelines load initially after restarts", testTimelineLoadInitialEvents(v3, aliceToken, len(allRooms), wantRooms, numTimelineEventsPerRoom))
	// inject some live events
	liveEvents := []roomEvents{
		{
			roomID: allRooms[0].roomID,
			events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "ping"}, latestTimestamp.Add(1*time.Minute)),
			},
		},
		{
			roomID: allRooms[1].roomID,
			events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "ping2"}, latestTimestamp.Add(2*time.Minute)),
			},
		},
	}
	// add these live events to the global view of the timeline
	allRooms[0].events = append(allRooms[0].events, liveEvents[0].events...)
	allRooms[1].events = append(allRooms[1].events, liveEvents[1].events...)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(liveEvents...),
		},
	})
	v2.waitUntilEmpty(t, alice)

	// now we want the new live rooms and then the most recent 2 rooms from before
	wantRooms = append([]roomEvents{
		allRooms[1], allRooms[0],
	}, wantRooms[0:2]...)

	t.Run("live events are added to the timeline initially", testTimelineLoadInitialEvents(v3, aliceToken, len(allRooms), wantRooms, numTimelineEventsPerRoom))
}

// Create 20 rooms and send A,B,C into each. Then bump various rooms "live streamed" from v2 and ensure
// the correct delta operations are sent e.g DELETE/INSERT/UPDATE.
func TestTimelinesLiveStream(t *testing.T) {
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, "")
	defer v2.close()
	defer v3.close()
	alice := "@TestTimelinesLiveStream_alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN_TestTimelinesLiveStream"
	// make 20 rooms, last room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 20)
	latestTimestamp := time.Now()
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(time.Duration(i) * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!TestTimelinesLiveStream_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, ts.Add(3*time.Second)),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, ts.Add(4*time.Second)),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, ts.Add(5*time.Second)),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, ts.Add(6*time.Second)),
			}...),
		}
		if ts.After(latestTimestamp) {
			latestTimestamp = ts.Add(10 * time.Second)
		}
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})
	numTimelineEventsPerRoom := 3

	// send a live event in allRooms[i] (always 1s newer)
	bumpRoom := func(i int) {
		latestTimestamp = latestTimestamp.Add(1 * time.Second)
		ev := testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": fmt.Sprintf("bump %d", i)}, latestTimestamp)
		allRooms[i].events = append(allRooms[i].events, ev)
		v2.queueResponse(alice, sync2.SyncResponse{
			Rooms: sync2.SyncRoomsResponse{
				Join: v2JoinTimeline(roomEvents{
					roomID: allRooms[i].roomID,
					events: []json.RawMessage{ev},
				}),
			},
		})
		v2.waitUntilEmpty(t, alice)
	}

	// most recent 4 rooms
	var wantRooms []roomEvents
	i := 0
	for len(wantRooms) < 4 {
		wantRooms = append(wantRooms, allRooms[len(allRooms)-i-1])
		i++
	}

	// first request => rooms 19,18,17,16
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Rooms: sync3.SliceRanges{
			[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
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
					MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, wantRooms[i].events),
				)
				if err != nil {
					return err
				}
			}
			return nil
		}),
	))

	bumpRoom(7)

	// next request, DELETE 3; INSERT 0 7;
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Rooms: sync3.SliceRanges{
			[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
		},
		// sticky remember the timeline_limit
	})
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3DeleteOp(3),
		MatchV3InsertOp(
			0, allRooms[7].roomID,
			MatchRoomName(allRooms[7].name),
			MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, allRooms[7].events),
		),
	))

	bumpRoom(7)

	// next request, UPDATE 0 7;
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Rooms: sync3.SliceRanges{
			[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
		},
	})
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3UpdateOp(0, allRooms[7].roomID),
	))

	bumpRoom(18)

	// next request, DELETE 2; INSERT 0 18;
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Rooms: sync3.SliceRanges{
			[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
		},
	})
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3DeleteOp(2),
		MatchV3InsertOp(
			0, allRooms[18].roomID,
			MatchRoomName(allRooms[18].name),
			MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, allRooms[18].events),
		),
	))

}

// Executes a sync v3 request without a ?pos and asserts that the count, rooms and timeline events match the inputs given.
func testTimelineLoadInitialEvents(v3 *testV3Server, token string, count int, wantRooms []roomEvents, numTimelineEventsPerRoom int) func(t *testing.T) {
	return func(t *testing.T) {
		res := v3.mustDoV3Request(t, token, sync3.Request{
			Rooms: sync3.SliceRanges{
				[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
			},
			TimelineLimit: int64(numTimelineEventsPerRoom),
			SessionID:     t.Name(),
		})

		MatchResponse(t, res, MatchV3Count(count), MatchV3Ops(
			MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
				if len(op.Rooms) != len(wantRooms) {
					return fmt.Errorf("want %d rooms, got %d", len(wantRooms), len(op.Rooms))
				}
				for i := range wantRooms {
					err := wantRooms[i].MatchRoom(
						op.Rooms[i],
						MatchRoomName(wantRooms[i].name),
						MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, wantRooms[i].events),
					)
					if err != nil {
						return err
					}
				}
				return nil
			}),
		))
	}
}
