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

func TestInteg(t *testing.T) {
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, "")
	defer v2.close()
	defer v3.close()
	alice := "@TestInteg_alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN_TestInteg"
	roomA := "!a_TestInteg:localhost"
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				roomA: {
					Timeline: sync2.TimelineResponse{
						Events: []json.RawMessage{
							testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
							testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
							testutils.NewStateEvent(t, "m.room.join_rules", "", alice, map[string]interface{}{"join_rule": "public"}),
							testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "hello world"}, time.Now()),
						},
					},
				},
			},
		},
	})

	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Rooms: sync3.SliceRanges{
			[2]int64{0, 9}, // first 10 rooms
		},
	})
	MatchResponse(t, res, MatchV3Count(1), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != 1 {
				return fmt.Errorf("want 1 room, got %d", len(op.Rooms))
			}
			room := op.Rooms[0]
			if room.RoomID != roomA {
				return fmt.Errorf("want room id %s got %s", roomA, room.RoomID)
			}
			return nil
		}),
	))
}
