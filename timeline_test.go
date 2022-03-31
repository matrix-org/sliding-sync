package syncv3

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
	"github.com/tidwall/sjson"
)

// Inject 20 rooms with A,B,C as the most recent events. Then do a v3 request [0,3] with a timeline limit of 3
// and make sure we get scrolback for the 4 rooms we care about. Then, restart the server (so it repopulates caches)
// and attempt the same request again, making sure we get the same results. Then add in some "live" v2 events
// and make sure the initial scrollback includes these new live events.
func TestTimelines(t *testing.T) {
	// setup code
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	alice := "@TestTimelines_alice:localhost"
	// make 20 rooms, last room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 20)
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(time.Duration(i) * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!TestTimelines_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
			}...),
		}
	}
	latestTimestamp := time.Now().Add(10 * time.Hour)
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
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "ping"}, testutils.WithTimestamp(latestTimestamp.Add(1*time.Minute))),
			},
		},
		{
			roomID: allRooms[1].roomID,
			events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "ping2"}, testutils.WithTimestamp(latestTimestamp.Add(2*time.Minute))),
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
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
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
		ev := testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": fmt.Sprintf("bump %d", i)}, testutils.WithTimestamp(latestTimestamp))
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
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: int64(numTimelineEventsPerRoom),
			},
		}},
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
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
			},
			// sticky remember the timeline_limit
		}},
	})
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3DeleteOp(0, 3),
		MatchV3InsertOp(
			0, 0, allRooms[7].roomID,
			MatchRoomName(allRooms[7].name),
			MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, allRooms[7].events),
		),
	))

	bumpRoom(7)

	// next request, UPDATE 0 7;
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
			},
		}},
	})
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3UpdateOp(0, 0, allRooms[7].roomID),
	))

	bumpRoom(18)

	// next request, DELETE 2; INSERT 0 18;
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
			},
		}},
	})
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3DeleteOp(0, 2),
		MatchV3InsertOp(
			0, 0, allRooms[18].roomID,
			MatchRoomName(allRooms[18].name),
			MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, allRooms[18].events),
		),
	))

}

func TestInitialFlag(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				state:  createRoomState(t, alice, time.Now()),
			}),
		},
	})
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 10,
			},
		}},
	})
	MatchResponse(t, res, MatchV3Ops(
		MatchV3SyncOpWithMatchers(MatchRoomRange(
			[]roomMatcher{MatchRoomInitial(true)},
		)),
	))
	// send an update
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(
				roomEvents{
					roomID: roomID,
					events: []json.RawMessage{
						testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{}),
					},
				},
			),
		},
	})
	v2.waitUntilEmpty(t, alice)

	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
		}},
	})
	MatchResponse(t, res, MatchV3Ops(
		MatchV3UpdateOp(0, 0, roomID, MatchRoomInitial(false)),
	))
}

// Regression test for in-the-wild bug:
//   ERR missing events in database!
//   ERR V2: failed to accumulate room error="failed to extract nids from inserted events, asked for 9 got 8"
// We should be able to gracefully handle duplicate events in the timeline.
func TestDuplicateEventsInTimeline(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"

	dupeEvent := testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{})
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				state:  createRoomState(t, alice, time.Now()),
				events: []json.RawMessage{
					testutils.NewStateEvent(t, "m.room.topic", "", alice, map[string]interface{}{}),
					dupeEvent, dupeEvent,
				},
			}),
		},
	})
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 10,
			},
		}},
	})
	MatchResponse(t, res, MatchV3Ops(
		MatchV3SyncOpWithMatchers(MatchRoomRange(
			[]roomMatcher{
				MatchRoomTimelineMostRecent(1, []json.RawMessage{dupeEvent}),
			},
		)),
	))
}

// Regression test for https://github.com/matrix-org/sliding-sync/commit/39d6e99f967e55b609f8ef8b4271c04ebb053d37
// Request a timeline_limit of 0 for the room list. Sometimes when a new event arrives it causes an
// unrelated room to be sent to the client (e.g tracking rooms [5,10] and room 15 bumps to room 2,
// causing all the rooms to shift so you're now actually tracking [4,9] - the client knows 5-9 but
// room 4 is new, so you notify about that room and not the one which had a new event (room 15).
// Ensure that room 4 is given to the client. In the past, this would panic when timeline limit = 0
// as the timeline was loaded using the timeline limit of the client, and an unchecked array access
// into the timeline
func TestTimelineMiddleWindowZeroTimelineLimit(t *testing.T) {
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, "")
	defer v2.close()
	defer v3.close()
	// make 20 rooms, first room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 20)
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(-1 * time.Duration(i) * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!TestTimelineMiddleWindowZeroTimelineLimit_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
			}...),
		}
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// Request rooms 5-10 with a 0 timeline limit
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{5, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 0,
			},
		}},
	})
	wantRooms := allRooms[5:11]
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != len(wantRooms) {
				return fmt.Errorf("want %d rooms, got %d", len(wantRooms), len(op.Rooms))
			}
			for i := range wantRooms {
				err := wantRooms[i].MatchRoom(
					op.Rooms[i],
					MatchRoomName(wantRooms[i].name),
					MatchRoomTimeline(nil),
				)
				if err != nil {
					return err
				}
			}
			return nil
		}),
	))

	// bump room 15 to 2
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: allRooms[15].roomID,
				events: []json.RawMessage{
					testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "bump"}),
				},
			}),
		},
	})
	v2.waitUntilEmpty(t, alice)

	// should see room 4, the server should not panic
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{5, 10},
			},
		}},
	})
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3DeleteOp(0, 10),
		MatchV3InsertOp(0, 5, allRooms[4].roomID),
	))
}

// Test that transaction IDs come down the user's stream correctly in the case where 2 clients are
// in the same room.
func TestTimelineTxnID(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"
	latestTimestamp := time.Now()
	// Alice and Bob are in the same room
	room := roomEvents{
		roomID: roomID,
		events: append(
			createRoomState(t, alice, latestTimestamp),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
		),
	}
	v2.addAccount(alice, aliceToken)
	v2.addAccount(bob, bobToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(room),
		},
	})
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(room),
		},
	})

	aliceRes := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10},
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 2,
				},
			},
		},
	})
	bobRes := v3.mustDoV3Request(t, bobToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10},
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 2,
				},
			},
		},
	})

	// Alice has sent a message but it arrives down Bob's poller first, so it has no txn_id
	txnID := "m1234567890"
	newEvent := testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "hi"}, testutils.WithUnsigned(map[string]interface{}{
		"transaction_id": txnID,
	}))
	newEventNoUnsigned, err := sjson.DeleteBytes(newEvent, "unsigned")
	if err != nil {
		t.Fatalf("failed to delete bytes: %s", err)
	}
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{newEventNoUnsigned},
			}),
		},
	})
	v2.waitUntilEmpty(t, bob)

	// now it arrives down Alice's poller, but the event has already been persisted at this point!
	// We need a txn ID cache to remember it.
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{newEvent},
			}),
		},
	})
	v2.waitUntilEmpty(t, alice)

	// now Alice syncs, she should see the event with the txn ID
	aliceRes = v3.mustDoV3RequestWithPos(t, aliceToken, aliceRes.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10},
				},
			},
		},
	})
	MatchResponse(t, aliceRes, MatchV3Counts([]int{1}), MatchV3Ops(
		MatchV3UpdateOp(0, 0, roomID, MatchRoomID(roomID), MatchRoomTimelineMostRecent(1, []json.RawMessage{newEvent})),
	))

	// now Bob syncs, he should see the event without the txn ID
	bobRes = v3.mustDoV3RequestWithPos(t, bobToken, bobRes.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10},
				},
			},
		},
	})
	MatchResponse(t, bobRes, MatchV3Counts([]int{1}), MatchV3Ops(
		MatchV3UpdateOp(0, 0, roomID, MatchRoomID(roomID), MatchRoomTimelineMostRecent(1, []json.RawMessage{newEventNoUnsigned})),
	))

}

// Executes a sync v3 request without a ?pos and asserts that the count, rooms and timeline events match the inputs given.
func testTimelineLoadInitialEvents(v3 *testV3Server, token string, count int, wantRooms []roomEvents, numTimelineEventsPerRoom int) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		res := v3.mustDoV3Request(t, token, sync3.Request{
			Lists: []sync3.RequestList{{
				Ranges: sync3.SliceRanges{
					[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: int64(numTimelineEventsPerRoom),
				},
			}},
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

// Test that prev batch tokens appear correctly.
// 1: When there is no newer prev_batch, none is present.
// 2: When there is a newer prev_batch, it is present.
func TestPrevBatchInTimeline(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				prevBatch: "create",
				roomID:    roomID,
				state:     createRoomState(t, alice, time.Now()),
				events: []json.RawMessage{
					testutils.NewStateEvent(t, "m.room.topic", "", alice, map[string]interface{}{}),
					testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "hello"}),
				},
			}),
		},
	})
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 1,
			},
		}},
	})
	MatchResponse(t, res, MatchV3Ops(
		MatchV3SyncOpWithMatchers(MatchRoomRange(
			[]roomMatcher{
				MatchRoomID(roomID),
				MatchRoomPrevBatch(""),
			},
		)),
	))

	// now make a newer prev_batch and try again
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				prevBatch: "newer",
				roomID:    roomID,
				events: []json.RawMessage{
					testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "hello 2"}),
				},
			}),
		},
	})
	v2.waitUntilEmpty(t, alice)

	testCases := []struct {
		timelineLimit int64
		wantPrevBatch string
	}{
		{
			timelineLimit: 1,
			wantPrevBatch: "newer", // the latest event matches the start of the timeline for the new sync, so prev batches align
		},
		{
			timelineLimit: 2,
			// the end of the timeline for the initial sync, we do not have a prev batch for this event.
			// we cannot return 'create' here else we will miss the topic event before this event
			// hence we return the cloest prev batch which is later than this event and hope clients can
			// deal with dupes.
			wantPrevBatch: "newer",
		},
		{
			timelineLimit: 3,
			wantPrevBatch: "create", // the topic event, the start of the timeline for the initial sync, so prev batches align
		},
	}
	for _, tc := range testCases {
		res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
			Lists: []sync3.RequestList{{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10},
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: tc.timelineLimit,
				},
			}},
		})
		MatchResponse(t, res, MatchV3Ops(
			MatchV3SyncOpWithMatchers(MatchRoomRange(
				[]roomMatcher{
					MatchRoomID(roomID),
					MatchRoomPrevBatch(tc.wantPrevBatch),
				},
			)),
		))
	}
}
