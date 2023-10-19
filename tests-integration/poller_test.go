package syncv3

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/gjson"
)

// Tests that if Alice is syncing with Device A, then begins syncing on a new Device B, we use
// a custom filter on the first sync to just pull out to-device events (which is faster)
func TestSecondPollerFiltersToDevice(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	deviceAToken := "DEVICE_A_TOKEN"
	v2.addAccountWithDeviceID(alice, "A", deviceAToken)
	v2.queueResponse(deviceAToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: "!unimportant",
				events: createRoomState(t, alice, time.Now()),
			}),
		},
	})
	// seed the proxy with data and get the first poller running
	v3.mustDoV3Request(t, deviceAToken, sync3.Request{})

	// now sync with device B, and check we send the filter up
	deviceBToken := "DEVICE_B_TOKEN"
	v2.addAccountWithDeviceID(alice, "B", deviceBToken)
	seenInitialRequest := false
	v2.SetCheckRequest(func(token string, req *http.Request) {
		if token != deviceBToken {
			return
		}
		qps := req.URL.Query()
		since := qps.Get("since")
		filter := qps.Get("filter")
		t.Logf("CheckRequest: %v since=%v filter=%v", token, since, filter)
		if filter == "" {
			t.Errorf("expected a filter on all v2 syncs from poller, but got none")
			return
		}
		filterJSON := gjson.Parse(filter)
		timelineLimit := filterJSON.Get("room.timeline.limit").Int()
		roomsFilter := filterJSON.Get("room.rooms")

		if !seenInitialRequest {
			// First poll: should be an initial sync, limit 1, excluding all room timelines.
			if since != "" {
				t.Errorf("Expected no since token on first poll, but got %v", since)
			}
			if timelineLimit != 1 {
				t.Errorf("Expected timeline limit of 1 on first poll, but got %d", timelineLimit)
			}
			if !roomsFilter.Exists() {
				t.Errorf("Expected roomsFilter set to empty list on first poll, but got no roomFilter")
			}
			if len(roomsFilter.Array()) != 0 {
				t.Errorf("Expected roomsFilter set to empty list on first poll, but got %v", roomsFilter.Raw)
			}
		} else {
			// Second poll: should be an incremental sync, limit 50, including all room timelines.
			if since == "" {
				t.Errorf("Expected nonempty since token on second poll, but got empty")
			}
			if timelineLimit != 50 {
				t.Errorf("Expected timeline limit of 50 on second poll, but got %d", timelineLimit)
			}
			if roomsFilter.Exists() {
				t.Errorf("Expected missing roomsFilter on second poll, but got %v", roomsFilter.Raw)
			}
		}

		seenInitialRequest = true
	})

	wantMsg := json.RawMessage(`{"type":"f","content":{"f":"b"}}`)
	v2.queueResponse(deviceBToken, sync2.SyncResponse{
		NextBatch: "a",
		ToDevice: sync2.EventsResponse{
			Events: []json.RawMessage{
				wantMsg,
			},
		},
	})
	boolTrue := true
	res := v3.mustDoV3Request(t, deviceBToken, sync3.Request{
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Core: extensions.Core{Enabled: &boolTrue},
			},
		},
	})

	if !seenInitialRequest {
		t.Fatalf("did not see initial request for 2nd device")
	}
	// the first request will not wait for the response before returning due to device A. Poll again
	// and now we should see the to-device msg.
	res = v3.mustDoV3RequestWithPos(t, deviceBToken, res.Pos, sync3.Request{})
	m.MatchResponse(t, res, m.MatchToDeviceMessages([]json.RawMessage{wantMsg}))
}

// Test that the poller makes a best-effort attempt to integrate state seen in a
// v2 sync state block. Our strategy for doing so is to prepend any unknown state events
// to the start of the v2 sync response's timeline, which should then be visible to
// sync v3 clients as ordinary state events in the room timeline.
func TestPollerHandlesUnknownStateEventsOnIncrementalSync(t *testing.T) {
	// FIXME: this should resolve once we update downstream caches
	t.Skip("We will never see the name/PL event in the timeline with the new code due to those events being part of the state block.")
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	v2.addAccount(t, alice, aliceToken)
	const roomID = "!unimportant"
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: createRoomState(t, alice, time.Now()),
			}),
		},
	})
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: [][2]int64{{0, 20}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 10,
				},
			},
		},
	})

	t.Log("The poller receives a gappy incremental sync response with a state block. The power levels and room name have changed.")
	nameEvent := testutils.NewStateEvent(
		t,
		"m.room.name",
		"",
		alice,
		map[string]interface{}{"name": "banana"},
	)
	powerLevelsEvent := testutils.NewStateEvent(
		t,
		"m.room.power_levels",
		"",
		alice,
		map[string]interface{}{
			"users":          map[string]int{alice: 100},
			"events_default": 10,
		},
	)
	messageEvent := testutils.NewMessageEvent(t, alice, "hello")
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				roomID: {
					State: sync2.EventsResponse{
						Events: []json.RawMessage{nameEvent, powerLevelsEvent},
					},
					Timeline: sync2.TimelineResponse{
						Events:    []json.RawMessage{messageEvent},
						Limited:   true,
						PrevBatch: "batchymcbatchface",
					},
				},
			},
		},
	})

	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{})
	m.MatchResponse(
		t,
		res,
		m.MatchRoomSubscription(
			roomID,
			func(r sync3.Room) error {
				// syncv2 doesn't assign any meaning to the order of events in a state
				// block, so check for both possibilities
				nameFirst := m.MatchRoomTimeline([]json.RawMessage{nameEvent, powerLevelsEvent, messageEvent})
				powerLevelsFirst := m.MatchRoomTimeline([]json.RawMessage{powerLevelsEvent, nameEvent, messageEvent})
				if nameFirst(r) != nil && powerLevelsFirst(r) != nil {
					return fmt.Errorf("did not see state before message")
				}
				return nil
			},
			m.MatchRoomName("banana"),
		),
	)
}

// Similar to TestPollerHandlesUnknownStateEventsOnIncrementalSync. Here we are testing
// that if Alice's poller sees Bob leave in a state block, the events seen in that
// timeline are not visible to Bob.
func TestPollerUpdatesRoomMemberTrackerOnGappySyncStateBlock(t *testing.T) {
	// the room state should update to make bob no longer be a member, which should update downstream caches
	// DO WE SEND THESE GAPPY STATES TO THE CLIENT? It's NOT part of the timeline, but we need to let the client
	// know somehow? I think the best case here would be to invalidate that _room_ (if that were possible in the API)
	// to force the client to resync the state.
	t.Skip("figure out what the valid thing to do here is")
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	v2.addAccount(t, alice, aliceToken)
	v2.addAccount(t, bob, bobToken)
	const roomID = "!unimportant"

	t.Log("Alice and Bob's pollers initial sync. Both see the same state: that Alice and Bob share a room.")
	initialTimeline := createRoomState(t, alice, time.Now())
	bobJoin := testutils.NewStateEvent(
		t,
		"m.room.member",
		bob,
		bob,
		map[string]interface{}{"membership": "join"},
	)
	initialJoinBlock := v2JoinTimeline(roomEvents{
		roomID: roomID,
		events: append(initialTimeline, bobJoin),
	})
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{Join: initialJoinBlock},
	})
	v2.queueResponse(bobToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{Join: initialJoinBlock},
	})

	t.Log("Alice makes an initial sliding sync request.")
	syncRequest := sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: [][2]int64{{0, 20}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 10,
				},
			},
		},
	}
	aliceRes := v3.mustDoV3Request(t, aliceToken, syncRequest)

	t.Log("Alice sees herself and Bob joined to the room.")
	m.MatchResponse(
		t,
		aliceRes,
		m.MatchList(
			"a",
			m.MatchV3Count(1),
			m.MatchV3Ops(m.MatchV3SyncOp(0, 0, []string{roomID})),
		),
		m.MatchRoomSubscription(roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{bobJoin})),
	)

	t.Log("Bob makes an initial sliding sync request.")
	bobRes := v3.mustDoV3Request(t, bobToken, syncRequest)

	t.Log("Bob sees himself and Alice joined to the room.")
	m.MatchResponse(
		t,
		bobRes,
		m.MatchList(
			"a",
			m.MatchV3Count(1),
			m.MatchV3Ops(m.MatchV3SyncOp(0, 0, []string{roomID})),
		),
		m.MatchRoomSubscription(roomID, m.MatchJoinCount(2)),
	)

	t.Log("Alice's poller receives a gappy incremental sync response. Bob has left in the gap. The timeline includes a message from Alice.")
	bobLeave := testutils.NewStateEvent(
		t,
		"m.room.member",
		bob,
		bob,
		map[string]interface{}{"membership": "leave"},
	)
	aliceMessage := testutils.NewMessageEvent(t, alice, "hello")
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				roomID: {
					State: sync2.EventsResponse{
						Events: []json.RawMessage{bobLeave},
					},
					Timeline: sync2.TimelineResponse{
						Events:    []json.RawMessage{aliceMessage},
						Limited:   true,
						PrevBatch: "batchymcbatchface",
					},
				},
			},
		},
	})

	t.Log("Bob makes an incremental sliding sync request.")
	bobRes = v3.mustDoV3RequestWithPos(t, bobToken, bobRes.Pos, sync3.Request{})
	t.Log("He should see his leave event in the room timeline.")
	m.MatchResponse(
		t,
		bobRes,
		m.MatchList("a", m.MatchV3Count(1)),
		m.MatchRoomSubscription(roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{bobLeave})),
	)
}

func TestPollersCanBeResumedAfterExpiry(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()

	// Start the mock sync v2 server and add a device for alice and for bob.
	v2 := runTestV2Server(t)
	defer v2.close()
	const aliceDevice = "alice_phone"
	const bobDevice = "bob_desktop"
	v2.addAccountWithDeviceID(alice, aliceDevice, aliceToken)
	v2.addAccountWithDeviceID(bob, bobDevice, bobToken)

	// Queue up a sync v2 response for both Alice and Bob.
	v2.queueResponse(aliceToken, sync2.SyncResponse{NextBatch: "alice_response_1"})
	v2.queueResponse(bobToken, sync2.SyncResponse{NextBatch: "bob_response_1"})

	// Inject an old token from Alice and a new token from Bob into the DB.
	v2Store := sync2.NewStore(pqString, os.Getenv("SYNCV3_SECRET"))
	err := sqlutil.WithTransaction(v2Store.DB, func(txn *sqlx.Tx) (err error) {
		err = v2Store.DevicesTable.InsertDevice(txn, alice, aliceDevice)
		if err != nil {
			return
		}
		err = v2Store.DevicesTable.InsertDevice(txn, bob, bobDevice)
		if err != nil {
			return
		}
		_, err = v2Store.TokensTable.Insert(txn, aliceToken, alice, aliceDevice, time.UnixMicro(0))
		if err != nil {
			return
		}
		_, err = v2Store.TokensTable.Insert(txn, bobToken, bob, bobDevice, time.Now())
		return
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Start the v3 server and its pollers.")
	v3 := runTestServer(t, v2, pqString)
	go v3.h2.StartV2Pollers()
	defer v3.close()

	t.Log("Alice's poller should be active.")
	v2.waitUntilEmpty(t, aliceToken)
	t.Log("Bob's poller should be active.")
	v2.waitUntilEmpty(t, bobToken)

	t.Log("Manually trigger a poller cleanup.")
	v3.h2.ExpireOldPollers()

	t.Log("Queue up a sync v2 response for both Alice and Bob. Alice's response includes account data.")
	accdata := testutils.NewAccountData(t, "dummytype", map[string]any{})
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		NextBatch: "alice_response_2",
		AccountData: sync2.EventsResponse{
			Events: []json.RawMessage{
				accdata,
			},
		},
	})
	v2.queueResponse(bobToken, sync2.SyncResponse{NextBatch: "bob_response_2"})

	t.Log("Wait for Bob's poller to poll")
	v2.waitUntilEmpty(t, bobToken)

	// Alice's poller has likely already made an HTTP response. But her poller should
	// have been terminated before the request was received, so its since token
	// should not have been persisted to the DB.
	t.Log("Alice's since token in the DB should not have advanced.")
	// TODO: surprising that there isn't a function to get the since token for a device!
	var since string
	err = v2Store.DB.Get(&since, `SELECT since FROM syncv3_sync2_devices WHERE user_id = $1 AND device_id = $2`, alice, aliceDevice)
	if err != nil {
		t.Fatal(err)
	}
	if since != "alice_response_1" {
		t.Errorf("Alice's sync token in DB was %s, expected alice_response_1", since)
	}

	t.Log("Requeue the same response for Alice's restarted poller to consume.")
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		NextBatch: "alice_response_2",
		AccountData: sync2.EventsResponse{
			Events: []json.RawMessage{
				accdata,
			},
		},
	})

	t.Log("Alice makes a new sliding sync request")
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Extensions: extensions.Request{
			AccountData: &extensions.AccountDataRequest{
				Core: extensions.Core{
					Enabled: &boolTrue,
				},
			},
		},
	})

	t.Log("Alice's poller should have been polled.")
	v2.waitUntilEmpty(t, aliceToken)

	t.Log("Alice should see her account data")
	m.MatchResponse(t, res, m.MatchAccountData([]json.RawMessage{accdata}, nil))

}

// Regression test for https://github.com/matrix-org/sliding-sync/issues/287#issuecomment-1706522718
func TestPollerExpiryEnsurePollingRace(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	defer v2.close()
	v3 := runTestServer(t, v2, pqString)
	defer v3.close()

	v2.addAccount(t, alice, aliceToken)

	// Arrange the following:
	// 1. A request arrives from an unknown token.
	// 2. The API makes a /whoami lookup for the new token. That returns without error.
	// 3. The old token expires.
	// 4. The poller tries to call /sync but finds that the token has expired.

	v2.SetCheckRequest(func(token string, req *http.Request) {
		if token != aliceToken {
			t.Fatalf("unexpected poll from %s", token)
		}
		// Expire the token before we process the request.
		t.Log("Alice's token expires.")
		v2.invalidateTokenImmediately(token)
	})

	t.Log("Alice makes a sliding sync request with a token that's about to expire.")
	_, resBytes, status := v3.doV3Request(t, context.Background(), aliceToken, "", sync3.Request{})
	if status != http.StatusUnauthorized {
		t.Fatalf("Should have got 401 http response; got %d\n%s", status, resBytes)
	}
}

// Regression test for the bugfix for https://github.com/matrix-org/sliding-sync/issues/287#issuecomment-1706522718
// Specifically, we could cache the failure and never tell the poller about new tokens, wedging the client(!). This
// seems to have been due to the following:
//   - client hits sync for the first time. We /whoami and remember the token->user mapping in TokensTable.
//   - client syncing + poller syncing, everything happy.
//   - token expires. OnExpiredToken is sent to EnsurePoller which removes the entry from EnsurePoller and nukes the conns.
//   - client hits sync, gets 400 M_UNKNOWN_POS due to nuked conns.
//   - client hits a fresh /sync: for whatever reason, the token is NOT 401d there and then by the /whoami lookup failing.
//     Maybe failed to remove the token, but don't see any logs to suggest this. Seems to be an OIDC thing.
//   - EnsurePoller starts a poller, which immediately 401s as the token is expired.
//   - OnExpiredToken is sent first, which removes the entry in EnsurePoller.
//   - OnInitialSyncComplete[success=false] is sent after, which MAKES A NEW ENTRY with success=false.
//   - proxy sends back 401 M_UNKNOWN_TOKEN.
//   - At this point the proxy is wedged. Any token, no matter how valid they are, will not hit EnsurePoller because
//     we cached success=false for that (user,device).
//
// Traceable in the logs which show spam of this log line without "Poller: v2 poll loop started" interleaved.
//
//	12:45:33 ERR EnsurePolling failed, returning 401 conn=encryption device=xx user=@xx:xx.xx
//
// To test this failure mode we:
// - Create Alice and sync her poller.
// - Expire her token immediately, just like the test TestPollerExpiryEnsurePollingRace
// - Do another request with a valid new token, this should succeed.
func TestPollerExpiryEnsurePollingRaceDoesntWedge(t *testing.T) {
	newToken := "NEW_ALICE_TOKEN"
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	defer v2.close()
	v3 := runTestServer(t, v2, pqString)
	defer v3.close()

	v2.addAccount(t, alice, aliceToken)

	// Arrange the following:
	// 1. A request arrives from an unknown token.
	// 2. The API makes a /whoami lookup for the new token. That returns without error.
	// 3. The old token expires.
	// 4. The poller tries to call /sync but finds that the token has expired.
	// NEW 5. Using a "new token" works.

	var gotNewToken atomic.Bool
	v2.SetCheckRequest(func(token string, req *http.Request) {
		if token == newToken {
			t.Log("recv new token")
			gotNewToken.Store(true)
			return
		}
		if token != aliceToken {
			t.Fatalf("unexpected poll from %s", token)
		}
		// Expire the token before we process the request.
		t.Log("Alice's token expires.")
		v2.invalidateTokenImmediately(token)
	})

	t.Log("Alice makes a sliding sync request with a token that's about to expire.")
	_, resBytes, status := v3.doV3Request(t, context.Background(), aliceToken, "", sync3.Request{})
	if status != http.StatusUnauthorized {
		t.Fatalf("Should have got 401 http response; got %d\n%s", status, resBytes)
	}
	// make a new token and use it
	v2.addAccount(t, alice, newToken)
	_, resBytes, status = v3.doV3Request(t, context.Background(), newToken, "", sync3.Request{})
	if status != http.StatusOK {
		t.Fatalf("Should have got 200 http response; got %d\n%s", status, resBytes)
	}
	if !gotNewToken.Load() {
		t.Fatalf("never saw a v2 poll with the new token")
	}
}

func TestTimelineStopsLoadingWhenMissingPrevious(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	const roomID = "!unimportant"

	t.Log("Alice creates a room.")
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: "!unimportant",
				events: createRoomState(t, alice, time.Now()),
			}),
		},
	})

	t.Log("Alice syncs, starting a poller.")
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 10,
			},
		},
	})

	t.Log("Her response includes the room she created..")
	m.MatchResponse(t, res, m.MatchRoomSubscription(roomID))

	t.Log("Alice's poller receives a gappy sync with a timeline event.")
	msgAfterGap := testutils.NewMessageEvent(t, alice, "school's out for summer")
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				roomID: {
					Timeline: sync2.TimelineResponse{
						Events:    []json.RawMessage{msgAfterGap},
						Limited:   true,
						PrevBatch: "dummyPrevBatch",
					},
				},
			},
		},
	})
	v2.waitUntilEmpty(t, aliceToken)

	t.Log("Alice makes a new connection and syncs, requesting the last 10 timeline events.")
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		ConnID: "conn2",
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 10,
			},
		},
	})

	t.Log("The response's timeline should only include the event after the gap.")
	m.MatchResponse(t, res, m.MatchRoomSubscription(roomID,
		m.MatchRoomTimeline([]json.RawMessage{msgAfterGap}),
		m.MatchRoomPrevBatch("dummyPrevBatch"),
	))
}

// The "prepend state events" mechanism added in
// https://github.com/matrix-org/sliding-sync/pull/71 ensured that the proxy
// communicated state events in "gappy syncs" to users. But it did so via Accumulate,
// which made one snapshot for each state event. This was not an accurate model of the
// room's history (the state block comes in no particular order) and had awful
// performance for large gappy states.
//
// We now want to handle these in Initialise, making a single snapshot for the state
// block. This test ensures that is the case. The logic is very similar to the e2e test
// TestGappyState.
func TestGappyStateDoesNotAccumulateTheStateBlock(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	defer v2.close()
	v3 := runTestServer(t, v2, pqString)
	defer v3.close()

	v2.addAccount(t, alice, aliceToken)
	v2.addAccount(t, bob, bobToken)

	t.Log("Alice creates a room, sets its name and sends a message.")
	const roomID = "!unimportant"
	name1 := testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]any{
		"name": "wonderland",
	})
	msg1 := testutils.NewMessageEvent(t, alice, "0118 999 881 999 119 7253")

	joinTimeline := v2JoinTimeline(roomEvents{
		roomID: roomID,
		events: append(
			createRoomState(t, alice, time.Now()),
			name1,
			msg1,
		),
	})
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: joinTimeline,
		},
	})

	t.Log("Alice sliding syncs with a huge timeline limit, subscribing to the room she just created.")
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {TimelineLimit: 100},
		},
	})

	t.Log("Alice sees the room with the expected name, with the name event and message at the end of the timeline.")
	m.MatchResponse(t, res, m.MatchRoomSubscription(roomID,
		m.MatchRoomName("wonderland"),
		m.MatchRoomTimelineMostRecent(2, []json.RawMessage{name1, msg1}),
	))

	t.Log("Alice's poller receives a gappy sync, including a room name change, bob joining, and two messages.")
	stateBlock := make([]json.RawMessage, 0)
	for i := 0; i < 10; i++ {
		statePiece := testutils.NewStateEvent(t, "com.example.custom", fmt.Sprintf("%d", i), alice, map[string]any{})
		stateBlock = append(stateBlock, statePiece)
	}
	name2 := testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]any{
		"name": "not wonderland",
	})
	bobJoin := testutils.NewJoinEvent(t, bob)
	stateBlock = append(stateBlock, name2, bobJoin)

	msg2 := testutils.NewMessageEvent(t, alice, "Good morning!")
	msg3 := testutils.NewMessageEvent(t, alice, "That's a nice tnetennba.")
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				roomID: {
					State: sync2.EventsResponse{
						Events: stateBlock,
					},
					Timeline: sync2.TimelineResponse{
						Events:    []json.RawMessage{msg2, msg3},
						Limited:   true,
						PrevBatch: "dummyPrevBatch",
					},
				},
			},
		},
	})
	v2.waitUntilEmpty(t, aliceToken)

	t.Log("Alice should see the two most recent message in the timeline only. The room name should change too.")
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{})
	m.MatchResponse(t, res, m.MatchRoomSubscription(roomID,
		m.MatchRoomName("not wonderland"),
		// In particular, we shouldn't see state here because it's not part of the timeline.
		// Nor should we see msg1, as that comes before a gap.
		m.MatchRoomTimeline([]json.RawMessage{msg2, msg3}),
	))
}

// Right, this has turned out to be very involved. This test
//
// There are three varying parameters: Bert's initial membership in (3), his later
// membership in (5), and whether his sync in (6) is initial or long-polling ("live").
//
// 1. Registers two users Ana and Bert.
// 2. Has Ana create a public room.
// 3. Sets an initial membership for Bert in that room.
// 4. Sliding syncs for Bert, if he will live-sync in (6) below.
// 5. Gives Ana's poller a gappy poll in which Bert's membership changes.
// 6. Has Bert do a sliding sync.
// 7. Ana invites Bert to a DM.
//
// We perform the following assertions:
//   - After (3), Ana sees Bert's initial membership.
//   - If applicable: after (4), Bert sees his initial membership.
//   - After (5), Ana sees Bert's new membership.
//   - After (6), Bert sees his new membership (if there is anything to see).
//   - After (7), Bert sees the DM invite. This serves as a sentinel to prove that the proxy
//     has processed (5) in the case where there is nothing for Bert to see in (6), e.g.
//     a preemptive ban or an unban.
func TestClientsSeeMembershipTransitionsInGappyPolls(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	// TODO remove this? Otherwise running tests is sloooooow
	v2.timeToWaitForV2Response /= 20
	defer v2.close()
	v3 := runTestServer(t, v2, pqString)
	defer v3.close()

	type testcase struct {
		// Inputs
		beforeMembership string
		afterMembership  string
		viaLiveUpdate    bool
		// Scratch space
		id           string
		ana          string
		anaToken     string
		bert         string
		bertToken    string
		publicRoomID string // room that will receive gappy state
		dmRoomID     string // DM between ana and bert, used to send a sentinel message
	}

	var tcs []testcase

	transitions := map[string][]string{
		// before: {possible after}
		// https://spec.matrix.org/v1.8/client-server-api/#room-membership for the list of allowed transitions
		"none":   {"ban", "invite", "join", "leave"},
		"invite": {"ban", "join", "leave"},
		// Note: can also join->join here e.g. for displayname change, but will do that in a separate test
		"join":  {"ban", "leave"},
		"leave": {"ban", "invite", "join"},
		"ban":   {"leave"},
	}
	for before, afterOptions := range transitions {
		for _, after := range afterOptions {
			for _, live := range []bool{true, false} {
				idStr := fmt.Sprintf("%s-%s", before, after)
				if live {
					idStr += "-live"
				}

				tc := testcase{
					beforeMembership: before,
					afterMembership:  after,
					viaLiveUpdate:    live,
					id:               idStr,
					publicRoomID:     fmt.Sprintf("!%s-public", idStr),
					dmRoomID:         fmt.Sprintf("!%s-dm", idStr),
					// Using ana and bert to stop myself from pulling in package-level constants alice and bob
					ana:  fmt.Sprintf("@ana-%s:localhost", idStr),
					bert: fmt.Sprintf("@bert-%s:localhost", idStr),
				}
				tc.anaToken = tc.ana + "_token"
				tc.bertToken = tc.bert + "_token"
				tcs = append(tcs, tc)
			}
		}
	}

	bertReq := sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 10}},
			},
		},
	}

	setup := func(t *testing.T, tc testcase) (anaRes, bertRes *sync3.Response) {
		// TODO be explicit about pos this is icky.
		bertRes = &sync3.Response{}
		v2.addAccount(t, tc.ana, tc.anaToken)
		v2.addAccount(t, tc.bert, tc.bertToken)

		if tc.viaLiveUpdate {
			t.Log("Queue up an empty poller response for Bert, so the proxy considers him to be polling.")
			v2.queueResponse(tc.bertToken, sync2.SyncResponse{
				NextBatch: tc.bert + "_empty_sync",
			})
			bertRes = v3.mustDoV3Request(t, tc.bertToken, bertReq)
			v2.waitUntilEmpty(t, tc.bertToken)
		}

		// TODO: probably ought to have separate anas.
		t.Log("Ana creates a public room.")
		publicEvents := createRoomState(t, tc.ana, time.Now())

		var wantJoinCount int
		var wantInviteCount int
		switch tc.beforeMembership {
		case "none":
			t.Log("Bert has no membership in the room.")
			wantJoinCount = 1
			wantInviteCount = 0
		case "invite":
			t.Log("Bert is invited.")
			publicEvents = append(publicEvents, testutils.NewStateEvent(t, "m.room.member", tc.bert, tc.ana, map[string]any{"membership": "invite"}))
			wantJoinCount = 1
			wantInviteCount = 1
		case "join":
			t.Log("Bert joins the room.")
			publicEvents = append(publicEvents, testutils.NewJoinEvent(t, tc.bert))
			wantJoinCount = 2
			wantInviteCount = 0
		case "leave":
			t.Log("Bert is pre-emptively kicked.")
			publicEvents = append(publicEvents,
				testutils.NewStateEvent(t, "m.room.member", tc.bert, tc.ana, map[string]any{"membership": "leave"}),
			)
			wantJoinCount = 1
			wantInviteCount = 0
		case "ban":
			t.Log("Bert is banned.")
			publicEvents = append(publicEvents, testutils.NewStateEvent(t, "m.room.member", tc.bert, tc.ana, map[string]any{"membership": "ban"}))
			wantJoinCount = 1
			wantInviteCount = 0
		default:
			panic(fmt.Errorf("unknown beforeMembership %s", tc.beforeMembership))
		}

		t.Log("Ana's poller sees Bert's membership.")
		v2.queueResponse(tc.anaToken, sync2.SyncResponse{
			Rooms: sync2.SyncRoomsResponse{
				Join: map[string]sync2.SyncV2JoinResponse{
					tc.publicRoomID: {
						Timeline: sync2.TimelineResponse{
							Events:    publicEvents,
							PrevBatch: "anaPublicPrevBatch1",
						},
					},
				},
			},
			NextBatch: "anaSync1",
		})

		t.Log("Ana sliding syncs.")
		anaRes = v3.mustDoV3Request(t, tc.anaToken, sync3.Request{
			RoomSubscriptions: map[string]sync3.RoomSubscription{
				tc.publicRoomID: {TimelineLimit: 20},
			},
		})
		t.Log("She sees herself joined to both rooms, with appropriate timelines and counts.")
		// Note: we only expect timeline[1:] here, not the create event. See
		// https://github.com/matrix-org/sliding-sync/issues/343
		m.MatchResponse(t, anaRes,
			m.MatchRoomSubscription(tc.publicRoomID,
				m.MatchRoomTimeline(publicEvents[1:]),
				m.MatchJoinCount(wantJoinCount),
				m.MatchInviteCount(wantInviteCount),
			),
		)

		// If Bert is initially invited, make sure his poller sees the invite.
		if tc.beforeMembership == "invite" {
			t.Log("Bert's poller sees his invite.")
			v2.queueResponse(tc.bertToken, sync2.SyncResponse{
				Rooms: sync2.SyncRoomsResponse{
					Invite: map[string]sync2.SyncV2InviteResponse{
						tc.publicRoomID: {
							InviteState: sync2.EventsResponse{
								// TODO:  this really ought to be stripped state events
								Events: publicEvents,
							},
						},
					}},
				NextBatch: tc.bert + "_invite",
			})
			if tc.viaLiveUpdate {
				v2.waitUntilEmpty(t, tc.bertToken)
			}
		}
		return
	}

	anaGetsGappyPoll := func(t *testing.T, tc testcase, anaRes *sync3.Response) (newMembership json.RawMessage, publicTimeline []json.RawMessage) {
		t.Logf("Ana's poller gets a gappy sync response for the public room. Bert's membership is now %s, and Ana has sent 10 messages.", tc.afterMembership)
		publicTimeline = make([]json.RawMessage, 10)
		for i := range publicTimeline {
			publicTimeline[i] = testutils.NewMessageEvent(t, tc.ana, fmt.Sprintf("hello %d", i))
		}

		switch tc.afterMembership {
		case "invite":
			newMembership = testutils.NewStateEvent(t, "m.room.member", tc.bert, tc.ana, map[string]any{"membership": "invite"})
		case "join":
			newMembership = testutils.NewJoinEvent(t, tc.bert)
		case "leave":
			newMembership = testutils.NewStateEvent(t, "m.room.member", tc.bert, tc.ana, map[string]any{"membership": "leave"})
		case "ban":
			newMembership = testutils.NewStateEvent(t, "m.room.member", tc.bert, tc.ana, map[string]any{"membership": "ban"})
		default:
			panic(fmt.Errorf("unknown afterMembership %s", tc.afterMembership))
		}

		v2.queueResponse(tc.anaToken, sync2.SyncResponse{
			NextBatch: "ana2",
			Rooms: sync2.SyncRoomsResponse{
				Join: map[string]sync2.SyncV2JoinResponse{
					tc.publicRoomID: {
						State: sync2.EventsResponse{
							Events: []json.RawMessage{newMembership},
						},
						Timeline: sync2.TimelineResponse{
							Events:    publicTimeline,
							Limited:   true,
							PrevBatch: "anaPublicPrevBatch2",
						},
					},
				},
			},
		})
		v2.waitUntilEmpty(t, tc.anaToken)

		t.Log("Ana syncs, requesting Bob's membership. She sees it, and her new timeline.")
		anaRes = v3.mustDoV3RequestWithPos(t, tc.anaToken, anaRes.Pos, sync3.Request{
			RoomSubscriptions: map[string]sync3.RoomSubscription{
				tc.publicRoomID: {
					TimelineLimit: 20,
					RequiredState: [][2]string{{"m.room.member", tc.bert}},
				},
			},
		})
		m.MatchResponse(t, anaRes, m.MatchRoomSubscription(tc.publicRoomID,
			m.MatchRoomRequiredState([]json.RawMessage{newMembership}),
			m.MatchRoomTimelineMostRecent(len(publicTimeline), publicTimeline),
		))
		return
	}

	for _, tc := range tcs {
		t.Run(tc.id, func(t *testing.T) {
			// 1-3: Register Ana and Bert, create a public room as Ana, set Bob's initial membership.
			anaRes, bertRes := setup(t, tc)
			defer func() {
				// Cleanup these users once we're done with them.
				v2.invalidateTokenImmediately(tc.anaToken)
				v2.invalidateTokenImmediately(tc.bertToken)
			}()

			// 4: sliding sync for Bert, if he will live-sync in (6) below.
			if tc.viaLiveUpdate {
				t.Log("Bert sliding syncs.")
				bertRes = v3.mustDoV3RequestWithPos(t, tc.bertToken, bertRes.Pos, bertReq)

				// Bert will see the entire history of these rooms, so there shouldn't be any prev batch tokens.
				expectedSubscriptions := map[string][]m.RoomMatcher{}
				switch tc.beforeMembership {
				case "invite":
					t.Log("Bert sees his invite.")
					expectedSubscriptions[tc.publicRoomID] = []m.RoomMatcher{
						m.MatchRoomHasInviteState(),
						m.MatchInviteCount(1),
						m.MatchJoinCount(1),
						m.MatchRoomPrevBatch(""),
					}
				case "join":
					t.Log("Bert sees his join.")
					expectedSubscriptions[tc.publicRoomID] = []m.RoomMatcher{
						m.MatchRoomLacksInviteState(),
						m.MatchInviteCount(0),
						m.MatchJoinCount(2),
						m.MatchRoomPrevBatch(""),
					}
				case "none":
					fallthrough
				case "leave":
					fallthrough
				case "ban":
					t.Log("Bert does not see the room.")
				default:
					panic(fmt.Errorf("unknown beforeMembership %s", tc.beforeMembership))
				}
				m.MatchResponse(t, bertRes, m.MatchRoomSubscriptionsStrict(expectedSubscriptions))
			}

			// 5: Ana receives a gappy poll, plus a sentinel in her DM with Bert.
			newMembership, publicTimeline := anaGetsGappyPoll(t, tc, anaRes)

			// 6: Bert sliding syncs.
			t.Log("Bert sliding syncs.")
			bertRes = v3.mustDoV3RequestWithPos(t, tc.bertToken, bertRes.Pos, bertReq)

			// Work out what Bert should see.
			respMatchers := []m.RespMatcher{}

			switch tc.afterMembership {
			case "invite":
				t.Log("Bert should see his invite.")
				respMatchers = append(respMatchers,
					m.MatchList("a", m.MatchV3Count(1)),
					m.MatchRoomSubscription(tc.publicRoomID,
						m.MatchRoomHasInviteState(),
						m.MatchInviteCount(1),
						m.MatchJoinCount(1),
					))
			case "join":
				t.Log("Bert should see himself joined to the room, and Alice's messages.")
				respMatchers = append(respMatchers,
					m.MatchList("a", m.MatchV3Count(1)),
					m.MatchRoomSubscription(tc.publicRoomID,
						m.MatchRoomLacksInviteState(),
						m.MatchInviteCount(0),
						m.MatchJoinCount(2),
						m.MatchRoomTimelineMostRecent(len(publicTimeline), publicTimeline),
					))
			case "leave":
				fallthrough
			case "ban":
				respMatchers = append(respMatchers, m.MatchList("a", m.MatchV3Count(0)))

				// if both previous and after membership was neither join nor invite, don't expect to see the new membership.
				wasntJoinedNorInvited := tc.beforeMembership == "none" || tc.beforeMembership == "leave" || tc.beforeMembership == "ban"
				if wasntJoinedNorInvited {
					t.Logf("Bob shouldn't see his %s (membership was: %s)", tc.afterMembership, tc.beforeMembership)
					respMatchers = append(respMatchers, m.MatchRoomSubscriptionsStrict(nil))
				} else {
					t.Logf("Bob should see his %s (membership was: %s)", tc.afterMembership, tc.beforeMembership)
					respMatchers = append(respMatchers, m.MatchRoomSubscription(tc.publicRoomID,
						m.MatchRoomTimeline([]json.RawMessage{newMembership}),
					))
				}
			default:
				panic(fmt.Errorf("unknown afterMembership %s", tc.afterMembership))
			}

			m.MatchResponse(t, bertRes, respMatchers...)

			// 7: Ana invites Bert to a DM. He accepts.
			// This is a sentinel which proves the proxy has processed the gappy poll
			// properly in the situations where there's nothing for Bert to see in his
			// second sync, e.g. ban -> leave (an unban).
			t.Log("Ana invites Bert to a DM. He accepts.")
			bertDMJoin := testutils.NewJoinEvent(t, tc.bert)
			dmTimeline := append(
				createRoomState(t, tc.ana, time.Now()),
				testutils.NewStateEvent(t, "m.room.member", tc.bert, tc.ana, map[string]any{"membership": "invite"}),
				bertDMJoin,
			)
			v2.queueResponse(tc.anaToken, sync2.SyncResponse{
				NextBatch: "ana3",
				Rooms: sync2.SyncRoomsResponse{
					Join: map[string]sync2.SyncV2JoinResponse{
						tc.dmRoomID: {
							Timeline: sync2.TimelineResponse{
								Events:    dmTimeline,
								PrevBatch: "anaDM",
							},
						},
					},
				},
			})
			v2.waitUntilEmpty(t, tc.anaToken)

			t.Log("Bert sliding syncs")
			bertRes = v3.mustDoV3RequestWithPos(t, tc.bertToken, bertRes.Pos, bertReq)

			t.Log("Bert sees his join to the DM.")
			m.MatchResponse(t, bertRes, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
				tc.dmRoomID: {m.MatchRoomLacksInviteState(), m.MatchRoomTimelineMostRecent(1, []json.RawMessage{bertDMJoin})},
			}))
		})
	}

}

// This is a minimal version of the test above to work out wtf is going on.
func TestTimelineAfterRequestingStateAfterGappyPoll(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	defer v2.close()
	v3 := runTestServer(t, v2, pqString)
	defer v3.close()

	alice := "alice"
	aliceToken := "alicetoken"
	bob := "bob"
	roomID := "!unimportant"

	v2.addAccount(t, alice, aliceToken)

	t.Log("alice creates a public room.")
	timeline1 := createRoomState(t, alice, time.Now())
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				roomID: {
					Timeline: sync2.TimelineResponse{
						Events:    timeline1,
						PrevBatch: "alicePublicPrevBatch1",
					},
				},
			},
		},
		NextBatch: "aliceSync1",
	})

	t.Log("alice sliding syncs.")
	aliceRes := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {TimelineLimit: 20},
		},
	})

	t.Log("She sees herself joined to her room, with an appropriate timeline.")
	// Note: we only expect timeline1[1:] here, excluding the create event. See
	// https://github.com/matrix-org/sliding-sync/issues/343
	m.MatchResponse(t, aliceRes,
		m.LogResponse(t),
		m.MatchRoomSubscription(roomID, m.MatchRoomTimeline(timeline1[1:])),
	)

	t.Logf("Alice's poller gets a gappy sync response for the public room. bob's membership is now join, and alice has sent 10 messages.")
	timeline2 := make([]json.RawMessage, 10)
	for i := range timeline2 {
		timeline2[i] = testutils.NewMessageEvent(t, alice, fmt.Sprintf("hello %d", i))
	}

	newMembership := testutils.NewJoinEvent(t, bob)

	v2.queueResponse(aliceToken, sync2.SyncResponse{
		NextBatch: "alice2",
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				roomID: {
					State: sync2.EventsResponse{
						Events: []json.RawMessage{newMembership},
					},
					Timeline: sync2.TimelineResponse{
						Events:    timeline2,
						Limited:   true,
						PrevBatch: "alicePublicPrevBatch2",
					},
				},
			},
		},
	})
	v2.waitUntilEmpty(t, aliceToken)

	t.Log("alice syncs, requesting Bob's membership. She sees it, and her new timeline.")
	aliceRes = v3.mustDoV3RequestWithPos(t, aliceToken, aliceRes.Pos, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 20,
				RequiredState: [][2]string{{"m.room.member", bob}},
			},
		},
	})
	m.MatchResponse(t, aliceRes, m.MatchRoomSubscription(roomID,
		m.MatchRoomRequiredState([]json.RawMessage{newMembership}),
		m.MatchRoomTimeline(timeline2),
	))
}
