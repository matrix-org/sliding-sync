package syncv3

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// Test that if you /join a room and then immediately add a room subscription for said room before the
// proxy is aware of it, that you still get all the data for that room when it does arrive.
func TestRoomSubscriptionJoinRoomRace(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	raceRoom := roomEvents{
		roomID: "!race:localhost",
		events: createRoomState(t, alice, time.Now()),
	}
	// add the account and queue a dummy response so there is a poll loop and we can get requests serviced
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: "!unimportant",
				events: createRoomState(t, alice, time.Now()),
			}),
		},
	})
	// dummy request to start the poll loop
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(nil))

	// now make a room subscription for a room which does not yet exist from the proxy's pov
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			raceRoom.roomID: {
				RequiredState: [][2]string{
					{"m.room.create", ""},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(nil))

	// now the proxy becomes aware of it
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(raceRoom),
		},
	})
	v2.waitUntilEmpty(t, alice) // ensure we have processed it fully so we know it should exist

	// hit the proxy again with this connection, we should get the data
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		raceRoom.roomID: {
			m.MatchRoomInitial(true),
			m.MatchRoomRequiredState([]json.RawMessage{raceRoom.events[0]}), // the create event
		},
	}))
}
