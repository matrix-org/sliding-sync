package syncv3

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// - Test that you can get all rooms initially for a list
// - Test that you can get all rooms initially for a list with specific filters
// - Test that newly joined rooms still include all state
// - Test that live updates just send the event and no ops
func TestSlowGetAllRoomsInitial(t *testing.T) {
	boolTrue := true
	numTimelineEventsPerRoom := 3
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, "")
	defer v2.close()
	defer v3.close()
	// make 20 rooms, last room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 20)
	allRoomIDs := make([]string, len(allRooms))
	allRoomMatchers := make(map[string][]m.RoomMatcher)
	latestTimestamp := time.Now()
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(time.Duration(i) * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!TestSlowGetAllRoomsInitial_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
			}...),
		}
		allRoomIDs[i] = allRooms[i].roomID
		if ts.After(latestTimestamp) {
			latestTimestamp = ts.Add(10 * time.Second)
		}
		allRoomMatchers[allRooms[i].roomID] = []m.RoomMatcher{
			m.MatchRoomInitial(true),
			m.MatchRoomName(allRooms[i].name),
			m.MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, allRooms[i].events),
		}
	}
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})
	// fetch all the rooms!
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 3}, // these get ignored
				},
				SlowGetAllRooms: &boolTrue,
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: int64(numTimelineEventsPerRoom),
				},
			}},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(allRooms)), m.MatchV3Ops(
		m.MatchV3SyncOp(0, int64(len(allRooms)-1), allRoomIDs, true),
	)), m.MatchRoomSubscriptionsStrict(allRoomMatchers))

	// now redo this but with a room name filter
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 3}, // these get ignored
				},
				SlowGetAllRooms: &boolTrue,
				Filters: &sync3.RequestFilters{
					RoomNameFilter: "My Room 1", // returns 1,10,11,12,13,14 etc
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: int64(numTimelineEventsPerRoom),
				},
			}},
	})
	// remove rooms that don't have a leading 1 index as they should be filtered out
	for i := 0; i < 10; i++ {
		if i == 1 {
			continue
		}
		delete(allRoomMatchers, allRooms[i].roomID)
	}
	roomIDs := make([]string, 0, len(allRoomMatchers))
	for roomID := range allRoomMatchers {
		roomIDs = append(roomIDs, roomID)
	}
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(allRoomMatchers)), m.MatchV3Ops(
		m.MatchV3SyncOp(0, int64(len(allRoomMatchers)-1), roomIDs, true),
	)), m.MatchRoomSubscriptionsStrict(allRoomMatchers))

	t.Run("Newly joined rooms get all state", func(t *testing.T) {
		ts := latestTimestamp
		roomName := "My Room 111111"
		newRoom := roomEvents{
			roomID: fmt.Sprintf("!TestSlowGetAllRoomsInitial_%dNEW:localhost", len(allRooms)),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
			}...),
		}
		v2.queueResponse(alice, sync2.SyncResponse{
			Rooms: sync2.SyncRoomsResponse{
				Join: v2JoinTimeline(newRoom),
			},
		})
		v2.waitUntilEmpty(t, alice)
		// reuse the position from the room name filter test, we should get this new room
		res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
			Lists: map[string]sync3.RequestList{},
		})
		m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(allRoomMatchers)+1)), m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
			newRoom.roomID: {
				m.MatchRoomInitial(true),
				m.MatchRoomName(roomName),
				m.MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, newRoom.events),
			},
		}))
	})

	t.Run("live updates just send event", func(t *testing.T) {
		newEvent := testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "D"}, testutils.WithTimestamp(latestTimestamp.Add(6*time.Second)))
		v2.queueResponse(alice, sync2.SyncResponse{
			Rooms: sync2.SyncRoomsResponse{
				Join: v2JoinTimeline(roomEvents{
					roomID: allRooms[11].roomID, // 11 to be caught in the room name filter
					events: []json.RawMessage{newEvent},
				}),
			},
		})
		v2.waitUntilEmpty(t, alice)
		res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
			Lists: map[string]sync3.RequestList{},
		})
		m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(allRoomMatchers)+1)), m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
			allRooms[11].roomID: {
				m.MatchRoomInitial(false),
				m.MatchRoomTimelineMostRecent(1, []json.RawMessage{newEvent}),
			},
		}))
	})
}
