package syncv3

import (
	"encoding/json"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"testing"
	"time"
)

func TestSyncsAroundTokenRefresh(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)

	aliceToken1 := "alice_token_1"
	roomID := "!room:test"
	v2.addAccount(alice, aliceToken1)

	t.Log("Prepare to tell a poller using aliceToken1 that Alice created a room and that Bob joined it.")

	bobJoin := testutils.NewJoinEvent(t, bob)
	v2.queueResponse(aliceToken1, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				state:  createRoomState(t, alice, time.Now()),
				events: []json.RawMessage{bobJoin},
			}),
		},
	})
	t.Log("Alice makes an initial sliding sync.")
	res := v3.mustDoV3Request(t, aliceToken1, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {TimelineLimit: 10},
		},
	})

	t.Log("Alice should see Bob's membership")
	m.MatchResponse(t, res,
		m.MatchRoomSubscription(roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{bobJoin})),
	)

	t.Log("Alice refreshes her access token. The old one expires.")
	aliceToken2 := "alice_token_2"
	v2.addAccount(alice, aliceToken2)
	v2.invalidateToken(aliceToken1)

	t.Log("Prepare to tell a poller using aliceToken2 that Alice created a room and that Bob joined it.")
	bobMsg := testutils.NewMessageEvent(t, bob, "Hello, world!")
	v2.queueResponse(aliceToken2, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{bobMsg},
			}),
		},
	})

	t.Log("Alice makes an incremental sliding sync.")
	res = v3.mustDoV3RequestWithPos(t, aliceToken2, res.Pos, sync3.Request{})

	t.Log("Alice should see Bob's message")
	m.MatchResponse(t, res,
		m.MatchRoomSubscription(roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{bobMsg})),
	)

}
