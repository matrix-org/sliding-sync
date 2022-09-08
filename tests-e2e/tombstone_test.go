package syncv3_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils/m"
	"github.com/tidwall/gjson"
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
				RoomSubscription: sync3.RoomSubscription{
					RequiredState: [][2]string{{"m.room.create", ""}},
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
	t.Logf("old %s new %s", roomID, newRoomID)
	time.Sleep(100 * time.Millisecond) // let the proxy process it

	res = client.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: [][2]int64{{0, 1}},
			},
		},
	}, WithPos(res.Pos))
	var tombstoneEventID string
	// count is 1 as we are auto-joined to the upgraded room
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3DeleteOp(1),
		m.MatchV3InsertOp(0, newRoomID), // insert new room
		m.MatchV3DeleteOp(1),            // remove old room
	)), m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchRoomInitial(false),
			func(r sync3.Room) error {
				// last timeline event should be the tombstone
				lastEvent := r.Timeline[len(r.Timeline)-1]
				ev := gjson.ParseBytes(lastEvent)
				if ev.Get("type").Str != "m.room.tombstone" || !ev.Get("state_key").Exists() || ev.Get("state_key").Str != "" {
					return fmt.Errorf("last event wasn't a tombstone event: %v", string(lastEvent))
				}
				tombstoneEventID = ev.Get("event_id").Str
				return nil
			},
		},
		// 2nd MatchRoomSubscriptions so we can pull out the event ID from the old room
	}), m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
		newRoomID: {
			m.MatchRoomInitial(true),
			m.MatchJoinCount(1),
			func(r sync3.Room) error { // nest it so the previous matcher has time to set tombstoneEventID
				return MatchRoomRequiredState([]Event{
					{
						Type:     "m.room.create",
						StateKey: ptr(""),
						Content: map[string]interface{}{
							"room_version": "9",
							"predecessor": map[string]interface{}{
								"room_id":  roomID,
								"event_id": tombstoneEventID,
							},
							"creator": client.UserID,
						},
					},
				})(r)
			},
		},
	}))
}

func TestTombstoneWalking(t *testing.T) {}
