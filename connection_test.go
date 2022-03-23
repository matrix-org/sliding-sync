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
	alice := "@alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN"
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
