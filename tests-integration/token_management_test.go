package syncv3

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

func TestInvalidTokenReturnsMUnknownTokenError(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	_, respBytes, statusCode := v3.doV3Request(t, context.Background(), "invalid_token", "", sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 1}},
			},
		},
	})
	if statusCode != 401 {
		t.Errorf("got HTTP %v want 401", statusCode)
	}
	var jsonError struct {
		Err     string `json:"error"`
		ErrCode string `json:"errcode"`
	}
	if err := json.Unmarshal(respBytes, &jsonError); err != nil {
		t.Fatalf("failed to unmarshal error response into JSON: %v", string(respBytes))
	}
	wantErrCode := "M_UNKNOWN_TOKEN"
	if jsonError.ErrCode != wantErrCode {
		t.Errorf("errcode: got %v want %v", jsonError.ErrCode, wantErrCode)
	}

}

func TestSyncWithNewTokenAfterOldExpires(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	aliceToken1 := "alice_token_1"
	aliceToken2 := "alice_token_2"
	roomID := "!room:test"
	v2.addAccount(t, alice, aliceToken1)

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
		NextBatch: "after_alice_initial_poll",
	})
	t.Log("Alice makes an initial sliding sync.")
	req := sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {TimelineLimit: 10},
		},
	}
	res := v3.mustDoV3Request(t, aliceToken1, req)

	t.Log("Alice should see Bob's membership")
	m.MatchResponse(t, res,
		m.MatchRoomSubscription(roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{bobJoin})),
	)

	t.Log("From this point forward, the poller should not make any initial sync requests.")
	v2.SetCheckRequest(func(userID, token string, req *http.Request) {
		if userID != alice {
			t.Errorf("Got unexpected poll for %s, expected %s only", userID, alice)
		}
		switch token {
		case aliceToken1: // this is okay; we should return a token expiry response
		case aliceToken2: // this is also okay; we should provide a proper response
			t.Logf("Got poll for %s", token)
		default:
			t.Logf("Got unexpected poll for %s", token)
		}
		since := req.URL.Query().Get("since")
		if since == "" {
			t.Errorf("Got unexpected initial sync poll for token %s", token)
		}
	})

	t.Log("Alice refreshes her access token. The old one expires.")
	v2.addAccount(t, alice, aliceToken2)
	v2.invalidateToken(aliceToken1)

	t.Log("Alice makes an incremental sliding sync with the new token.")
	_, body, code := v3.doV3Request(t, context.Background(), aliceToken2, res.Pos, sync3.Request{})
	// TODO: in principle the proxy could remember the previous Pos and serve this
	// request immediately. For now we keep things simple and require the client to make
	// a new connection.
	t.Log("The connection should be expired.")
	if code != 400 {
		t.Errorf("got HTTP %d want 400", code)
	}
	if gjson.ParseBytes(body).Get("errcode").Str != "M_UNKNOWN_POS" {
		t.Errorf("got %v want errcode=M_UNKNOWN_POS", string(body))
	}

	t.Log("Prepare to tell a poller using aliceToken2 that Alice created a room and that Bob joined it.")
	bobMsg := testutils.NewMessageEvent(t, bob, "Hello, world!")
	v2.queueResponse(aliceToken2, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{bobMsg},
			}),
		},
		NextBatch: "after_alice_incremental_poll",
	})

	t.Log("Alice makes a new sliding sync connection with her new token.")
	res = v3.mustDoV3Request(t, aliceToken2, req)

	t.Log("Alice should see Bob's message.")
	m.MatchResponse(t, res,
		m.MatchRoomSubscription(roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{bobMsg})),
	)

}

func TestSyncWithNewTokenBeforeOldExpires(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	aliceToken1 := "alice_token_1"
	aliceToken2 := "alice_token_2"
	roomID := "!room:test"
	v2.addAccount(t, alice, aliceToken1)

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
		NextBatch: "after_alice_initial_poll",
	})
	t.Log("Alice makes an initial sliding sync.")
	req := sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {TimelineLimit: 10},
		},
	}
	res := v3.mustDoV3Request(t, aliceToken1, req)

	t.Log("Alice should see Bob's membership")
	m.MatchResponse(t, res,
		m.MatchRoomSubscription(roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{bobJoin})),
	)

	t.Log("From this point forward, the poller should not make any initial sync requests.")
	v2.SetCheckRequest(func(userID, token string, req *http.Request) {
		if userID != alice {
			t.Errorf("Got unexpected poll for %s, expected %s only", userID, alice)
		}
		switch token {
		case aliceToken1: // either is okay
		case aliceToken2:
			t.Logf("Got poll for %s", token)
		default:
			t.Errorf("Got unexpected poll for %s", token)
		}
		since := req.URL.Query().Get("since")
		if since == "" {
			t.Errorf("Got unexpected initial sync poll for token %s", token)
		}
	})

	t.Log("Alice refreshes her access token. The old one has yet to expire.")
	v2.addAccount(t, alice, aliceToken2)

	t.Log("Prepare to tell a poller using aliceToken1 that Alice created a room and that Bob joined it.")
	bobMsg := testutils.NewMessageEvent(t, bob, "Hello, world!")
	v2.queueResponse(aliceToken1, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{bobMsg},
			}),
		},
		NextBatch: "after_alice_incremental_poll",
	})

	t.Log("Alice makes an incremental sliding sync with the new token.")
	res = v3.mustDoV3RequestWithPos(t, aliceToken2, res.Pos, req)

	t.Log("Alice should see Bob's message.")
	m.MatchResponse(t, res,
		m.MatchRoomSubscription(roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{bobMsg})),
	)

}
