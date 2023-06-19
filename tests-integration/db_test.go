package syncv3

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	syncv3 "github.com/matrix-org/sliding-sync"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// Test that the proxy works fine with low max conns. Low max conns can be a problem
// if a request A needs 2 conns to respond and that blocks forward progress on the server,
// and the request can only obtain 1 conn.
func TestMaxDBConns(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	opts := syncv3.Opts{
		DBMaxConns: 3,
	}
	v3 := runTestServer(t, v2, pqString, opts)
	defer v2.close()
	defer v3.close()

	testMaxDBConns := func() {
		// make N users and drip feed some events, make sure they are all seen
		numUsers := 5
		var wg sync.WaitGroup
		wg.Add(numUsers)
		for i := 0; i < numUsers; i++ {
			go func(n int) {
				defer wg.Done()
				userID := fmt.Sprintf("@maxconns_%d:localhost", n)
				token := fmt.Sprintf("maxconns_%d", n)
				roomID := fmt.Sprintf("!maxconns_%d", n)
				v2.addAccount(t, userID, token)
				v2.queueResponse(userID, sync2.SyncResponse{
					Rooms: sync2.SyncRoomsResponse{
						Join: v2JoinTimeline(roomEvents{
							roomID: roomID,
							state:  createRoomState(t, userID, time.Now()),
						}),
					},
				})
				// initial sync
				res := v3.mustDoV3Request(t, token, sync3.Request{
					Lists: map[string]sync3.RequestList{"a": {
						Ranges: sync3.SliceRanges{
							[2]int64{0, 1},
						},
						RoomSubscription: sync3.RoomSubscription{
							TimelineLimit: 1,
						},
					}},
				})
				t.Logf("user %s has done an initial /sync OK", userID)
				m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
					m.MatchV3SyncOp(0, 0, []string{roomID}),
				)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
					roomID: {
						m.MatchJoinCount(1),
					},
				}))
				// drip feed and get update
				dripMsg := testutils.NewEvent(t, "m.room.message", userID, map[string]interface{}{
					"msgtype": "m.text",
					"body":    "drip drip",
				})
				v2.queueResponse(userID, sync2.SyncResponse{
					Rooms: sync2.SyncRoomsResponse{
						Join: v2JoinTimeline(roomEvents{
							roomID: roomID,
							events: []json.RawMessage{
								dripMsg,
							},
						}),
					},
				})
				t.Logf("user %s has queued the drip", userID)
				v2.waitUntilEmpty(t, userID)
				t.Logf("user %s poller has received the drip", userID)
				res = v3.mustDoV3RequestWithPos(t, token, res.Pos, sync3.Request{})
				m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
					roomID: {
						m.MatchRoomTimelineMostRecent(1, []json.RawMessage{dripMsg}),
					},
				}))
				t.Logf("user %s has received the drip", userID)
			}(i)
		}
		wg.Wait()
	}

	testMaxDBConns()
	v3.restart(t, v2, pqString, opts)
	testMaxDBConns()
}
