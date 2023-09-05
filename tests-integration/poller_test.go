package syncv3

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"net/http"
	"os"
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
				extensions.Core{
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
	_, _, code := v3.doV3Request(t, context.Background(), aliceToken, "", sync3.Request{})
	if code == 200 {
		t.Fatalf("Should have got non 200 http response")
	}
}
