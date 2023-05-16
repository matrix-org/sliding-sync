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

// Regression test for a panic in the wild when we tried to write to internal.RoomMetadata.ChildSpaceRooms and the map didn't exist.
func TestBecomingASpaceDoesntCrash(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!foo:bar"
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: createRoomState(t, alice, time.Now()),
			}),
		},
	})
	// let the proxy store the room
	v3.mustDoV3Request(t, aliceToken, sync3.Request{})
	// restart the proxy: at this point we may have a nil ChildSpaceRooms map
	v3.restart(t, v2, pqString)

	// check it by injecting a space child
	spaceChildEvent := testutils.NewStateEvent(t, "m.space.child", "!somewhere:else", alice, map[string]interface{}{
		"via": []string{"example.com"},
	})
	// TODO: we inject bob here because alice's sync stream seems to discard this response post-restart for unknown reasons
	v2.addAccount(t, bob, bobToken)
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{
					spaceChildEvent,
				},
			}),
		},
	})

	// we should be able to request the room without crashing
	v3.mustDoV3Request(t, bobToken, sync3.Request{})
	// we should see the data
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {TimelineLimit: 1},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchRoomTimeline([]json.RawMessage{spaceChildEvent}),
		},
	}))
}
