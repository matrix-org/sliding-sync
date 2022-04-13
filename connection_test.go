package syncv3

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
)

// Test that if you hit /sync and give up, we only start 1 connection.
// Relevant for when large accounts hit /sync for the first time and then time-out locally, and then
// hit /sync again without a ?pos=
func TestMultipleConnsAtStartup(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"
	v2.addAccount(alice, aliceToken)
	var res *sync3.Response
	var wg sync.WaitGroup
	wg.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer wg.Done()
		_, body, _ := v3.doV3Request(t, ctx, aliceToken, "", sync3.Request{
			Lists: []sync3.RequestList{{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10},
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 10,
				},
			}},
		})
		if body != nil {
			// we got sent a response but should've got nothing as we knifed the connection
			t.Errorf("got unexpected response: %s", string(body))
		}
	}()
	// wait until the proxy is waiting on v2. As this is the initial sync, we won't have a conn yet.
	v2.waitUntilEmpty(t, alice)
	// interrupt the connection
	cancel()
	wg.Wait()
	// respond to sync v2
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				state:  createRoomState(t, alice, time.Now()),
				events: []json.RawMessage{
					testutils.NewStateEvent(t, "m.room.topic", "", alice, map[string]interface{}{}),
				},
			}),
		},
	})

	// do another /sync
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
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
				MatchRoomID(roomID),
			},
		)),
	))
}

// Regression test for running the proxy server behind a reverse proxy.
// The problem: when using a reverse proxy and a client cancels a request, the reverse proxy may
// not terminate the upstream connection. If this happens, subsequent requests from the client will
// stack up in Conn behind a mutex until the cancelled request is processed. In reality, we want the
// cancelled request to be stopped entirely. Whilst we cannot force reverse proxies to terminate the
// connection, we _can_ check if there is an outstanding request holding the mutex, and if so, interrupt
// it at the application layer. This test ensures we do that.
func TestOutstandingRequestsGetCancelled(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	// Rooms A,B gets sent to the client initially, then we will send a 2nd blocking request with a 3s timeout.
	// During this time, we will send a 3rd request with modified sort operations to ensure that the proxy can
	// return an immediate response. We _should_ see the response promptly as the blocking request gets cancelled.
	// If it takes seconds to process this request, that implies it got stacked up behind a previous request,
	// failing the test.
	roomA := "!a:localhost" // name is A, older timestamp
	roomB := "!b:localhost" // name is B, newer timestamp
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomA,
				state:  createRoomState(t, alice, time.Now()),
				events: []json.RawMessage{
					testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "A"}),
				},
			}, roomEvents{
				roomID: roomB,
				state:  createRoomState(t, alice, time.Now().Add(time.Hour)),
				events: []json.RawMessage{
					testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "B"}, testutils.WithTimestamp(time.Now().Add(time.Hour))),
				},
			}),
		},
	})
	// first request to get some data
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 1},
			},
			Sort: []string{sync3.SortByName}, // A,B
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 1,
			},
		}},
	})
	MatchResponse(t, res, MatchV3Count(2), MatchV3Ops(
		MatchV3SyncOpWithMatchers(
			MatchRoomRange(
				[]roomMatcher{MatchRoomID(roomA)},
				[]roomMatcher{MatchRoomID(roomB)},
			),
		),
	))
	// now we do a blocking request, and a few ms later do another request which can be satisfied
	// using the same pos
	pos := res.Pos
	waitTimeMS := 3000
	startTime := time.Now()
	go func() {
		time.Sleep(100 * time.Millisecond)
		res2 := v3.mustDoV3RequestWithPos(t, aliceToken, pos, sync3.Request{
			Lists: []sync3.RequestList{{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 1},
				},
				Sort: []string{sync3.SortByRecency}, // B,A
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
			}},
		})
		MatchResponse(t, res2, MatchV3Count(2), MatchV3Ops(
			MatchV3InvalidateOp(0, 0, 1),
			MatchV3SyncOpWithMatchers(
				MatchRoomRange(
					[]roomMatcher{MatchRoomID(roomB)},
					[]roomMatcher{MatchRoomID(roomA)},
				),
			),
		))
		if time.Since(startTime) > time.Second {
			t.Errorf("took >1s to process request which should have been processed instantly, took %v", time.Since(startTime))
		}
	}()
	req := sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 1},
			},
		}},
	}
	req.SetTimeoutMSecs(waitTimeMS)
	res = v3.mustDoV3RequestWithPos(t, aliceToken, pos, req)
	if time.Since(startTime) > time.Second {
		t.Errorf("took >1s to process request which should have been interrupted before timing out, took %v", time.Since(startTime))
	}
	MatchResponse(t, res, MatchV3Count(2), MatchV3Ops())
}

// Regression test to ensure that ?timeout= isn't reset when live events come in.
func TestConnectionTimeoutNotReset(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	// One room gets sent v2 updates, one room does not. Room B gets updates, which, because
	// we are tracking alphabetically, causes those updates to not trigger a v3 response. This
	// used to reset the timeout though, so we will check to make sure it doesn't.
	roomA := "!a:localhost"
	roomB := "!b:localhost"
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomA,
				state:  createRoomState(t, alice, time.Now()),
				events: []json.RawMessage{
					testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "A"}),
				},
			},
				roomEvents{
					roomID: roomB,
					state:  createRoomState(t, alice, time.Now()),
					events: []json.RawMessage{
						testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "B"}),
					},
				}),
		},
	})
	// first request to get some data
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 0}, // first room only -> roomID
			},
			Sort: []string{sync3.SortByName},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 1,
			},
		}},
	})
	MatchResponse(t, res, MatchV3Count(2), MatchV3Ops(
		MatchV3SyncOpWithMatchers(
			MatchRoomRange(
				[]roomMatcher{
					MatchRoomID(roomA),
				},
			),
		),
	))
	// 2nd request with a 1s timeout
	req := sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 0},
			},
		}},
	}
	req.SetTimeoutMSecs(1000) // 1s
	// inject 4 events 500ms apart - if we reset the timeout each time then we will return late
	go func() {
		time.Sleep(10 * time.Millisecond)
		ticker := time.NewTicker(500 * time.Millisecond)
		i := 0
		for range ticker.C {
			if i > 3 {
				t.Logf("stopping update")
				break
			}
			t.Logf("sending update")
			v2.queueResponse(alice, sync2.SyncResponse{
				Rooms: sync2.SyncRoomsResponse{
					Join: v2JoinTimeline(roomEvents{
						roomID: roomB,
						events: []json.RawMessage{
							testutils.NewEvent(
								t, "m.room.message", alice, map[string]interface{}{"old": "msg"}, testutils.WithTimestamp(time.Now().Add(-2*time.Hour)),
							),
						},
					}),
				},
			})
			i++
		}
	}()
	startTime := time.Now()
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, req)
	dur := time.Since(startTime)
	if dur > (1500 * time.Millisecond) { // 0.5s leeway
		t.Fatalf("request took %v to complete, expected ~1s", dur)
	}
	MatchResponse(t, res, MatchV3Count(2), MatchV3Ops())

}
