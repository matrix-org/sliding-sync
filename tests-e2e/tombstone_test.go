package syncv3_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils/m"
)

func TestTombstonesFlag(t *testing.T) {
	client := registerNewUser(t)

	// create room
	roomID := client.CreateRoom(t, map[string]interface{}{})

	res := client.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: [][2]int64{{0, 1}},
				Filters: &sync3.RequestFilters{
					IsTombstoned: &boolFalse,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 1, []string{roomID}),
	)))
	upgradeRes := client.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "rooms", roomID, "upgrade"}, WithJSONBody(t, map[string]interface{}{
		"new_version": "9",
	}))
	var body map[string]interface{}
	if err := json.NewDecoder(upgradeRes.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %s", err)
	}
	newRoomID := body["replacement_room"].(string)
	time.Sleep(100 * time.Millisecond) // let the proxy process it

	res = client.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: [][2]int64{{0, 1}},
			},
		},
	}, WithPos(res.Pos))
	// count is 1 as we are auto-joined to the upgraded room
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3DeleteOp(1),
		m.MatchV3InsertOp(0, newRoomID), // insert new room
		m.MatchV3DeleteOp(1),            // remove old room
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchRoomInitial(false),
			MatchRoomTimeline([]Event{
				{
					Type:     "m.room.tombstone",
					StateKey: ptr(""),
				},
			}),
		},
		newRoomID: {
			m.MatchRoomInitial(true),
			m.MatchJoinCount(1),
		},
	}))
}

func TestTombstoneWalking(t *testing.T) {}
