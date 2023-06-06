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

func TestListsAsKeys(t *testing.T) {
	boolTrue := true
	rig := NewTestRig(t)
	defer rig.Finish()
	encryptedRoomID := "!TestListsAsKeys_encrypted:localhost"
	unencryptedRoomID := "!TestListsAsKeys_unencrypted:localhost"
	rig.SetupV2RoomsForUser(t, alice, NoFlush, map[string]RoomDescriptor{
		encryptedRoomID: {
			IsEncrypted: true,
		},
		unencryptedRoomID: {
			IsEncrypted: false,
		},
	})
	aliceToken := rig.Token(alice)
	// make an encrypted room list, then bump both rooms and send a 2nd request with zero data
	// and make sure that we see the encrypted room message only (so the filter is still active)
	res := rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"enc": {
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		"enc": {
			m.MatchV3Count(1),
		},
	}), m.MatchRoomSubscription(encryptedRoomID))

	rig.FlushText(t, alice, encryptedRoomID, "bump encrypted")
	rig.FlushText(t, alice, unencryptedRoomID, "bump unencrypted")

	res = rig.V3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{})
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		"enc": {
			m.MatchV3Count(1),
		},
	}), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		encryptedRoomID: {},
	}))
}

// Regression test for a cause of duplicate rooms in the room list.
// The pollers process unread counts _before_ events. It does this so if counts bump from 0->1 we don't
// tell clients, instead we wait for the event and then tell them both at the same time atomically.
// This is desirable as it means we don't have phantom notifications. However, it doesn't work reliably
// in the proxy because one device poller can return the event in one /sync response then the unread count
// arrives later on a different poller's sync response.
// This manifest itself as confusing, invalid DELETE/INSERT operations, causing rooms to be duplicated.
func TestUnreadCountMisordering(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	one := 1
	zero := 0
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	// Create 3 rooms with the following order, sorted by notification level
	// - A [1 unread count] most recent
	// - B [0 unread count]
	// - C [0 unread count]
	// Then send a new event in C -> [A,C,B]   DELETE 2, INSERT 1 C
	// Then send unread count in C++ -> [C,A,B] DELETE 1, INSERT 0 C // this might be suppressed
	// Then send something unrelated which will cause a resort. This will cause a desync in lists between client/server.
	// Then send unread count in C-- -> [A,C,B]  DELETE 0, INSERT 1 C <-- this makes no sense if the prev ops were suppressed.
	roomA := "!a:localhost"
	roomB := "!b:localhost"
	roomC := "!c:localhost"
	data := map[string]struct {
		latestTimestamp time.Time
		notifCount      int
	}{
		roomA: {
			latestTimestamp: time.Now().Add(20 * time.Second),
			notifCount:      1,
		},
		roomB: {
			latestTimestamp: time.Now().Add(30 * time.Second),
			notifCount:      0,
		},
		roomC: {
			latestTimestamp: time.Now().Add(10 * time.Second),
			notifCount:      0,
		},
	}
	var re []roomEvents
	for roomID, info := range data {
		info := info
		re = append(re, roomEvents{
			roomID: roomID,
			state:  createRoomState(t, alice, time.Now()),
			events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "latest msg"}, testutils.WithTimestamp(info.latestTimestamp)),
			},
			notifCount: &info.notifCount,
		})
	}
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(re...),
		},
	})

	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: [][2]int64{{0, 5}},
				Sort:   []string{sync3.SortByNotificationLevel, sync3.SortByRecency},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(3), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 2, []string{roomA, roomB, roomC}),
	))) // A,B,C SYNC

	// Then send a new event in C -> [A,C,B]   DELETE 2, INSERT 1 C
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomC,
				events: []json.RawMessage{
					testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "bump"}, testutils.WithTimestamp(time.Now().Add(time.Second*40))),
				},
			}),
		},
	})
	v2.waitUntilEmpty(t, aliceToken)
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(3), m.MatchV3Ops(
		m.MatchV3DeleteOp(2), m.MatchV3InsertOp(1, roomC),
	)))

	// Then send unread count in C++ -> [C,A,B] DELETE 1, INSERT 0 C // this might be suppressed
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID:     roomC,
				notifCount: &one,
			}),
		},
	})
	v2.waitUntilEmpty(t, aliceToken)

	// Then send something unrelated which will cause a resort. This will cause a desync in lists between client/server.
	// This is unrelated because it doesn't affect sort position: B has same timestamp
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomB,
				events: []json.RawMessage{
					testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "bump 2"}, testutils.WithTimestamp(data[roomB].latestTimestamp)),
				},
			}),
		},
	})
	v2.waitUntilEmpty(t, aliceToken)

	// Then send unread count in C-- -> [A,C,B]  <-- this ends up being DEL 0, INS 1 C which is just wrong if we suppressed earlier.
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID:     roomC,
				notifCount: &zero,
			}),
		},
	})
	v2.waitUntilEmpty(t, aliceToken)

	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(3), m.MatchV3Ops(
		m.MatchV3DeleteOp(1), m.MatchV3InsertOp(0, roomC), // the unread count coming through
		m.MatchV3DeleteOp(0), m.MatchV3InsertOp(1, roomC), // the unread count decrease coming through
	)))
}

func TestBumpEventTypesOnStartup(t *testing.T) {
	ts := time.Now()
	t.Log("Alice makes three rooms.")
	r1CreateState := createRoomState(t, alice, ts)
	r1Timeline := []json.RawMessage{}

	// Add at least a second between significant events, to ensure there aren't any
	// timestamp clashes.
	ts = ts.Add(time.Second)
	r2CreateState := createRoomState(t, alice, ts)
	r2Timeline := []json.RawMessage{}

	ts = ts.Add(time.Second)
	r3CreateState := createRoomState(t, alice, ts)
	r3Timeline := []json.RawMessage{}

	t.Log("A series of events take place in all three rooms.")
	// The following sequence of events take place:
	// (1) r1: topic set
	// (2) r1: message
	// (3) r2: message
	// (4) r3: message
	// (5) r2: topic
	// (6) r1: profile change

	ts = ts.Add(time.Second)
	r1Timeline = append(r1Timeline,
		testutils.NewStateEvent(
			t, "m.room.topic", "", alice, map[string]interface{}{"topic": "potato"}, testutils.WithTimestamp(ts),
		),
	)

	ts = ts.Add(time.Second)
	r1Timeline = append(r1Timeline,
		testutils.NewMessageEvent(t, alice, "message in room 1", testutils.WithTimestamp(ts)),
	)

	ts = ts.Add(time.Second)
	r2Timeline = append(r2Timeline,
		testutils.NewMessageEvent(t, alice, "message in room 2", testutils.WithTimestamp(ts)),
	)

	ts = ts.Add(time.Second)
	r3Timeline = append(r3Timeline,
		testutils.NewMessageEvent(t, alice, "message in room 3", testutils.WithTimestamp(ts)),
	)

	ts = ts.Add(time.Second)
	r2Timeline = append(r2Timeline,
		testutils.NewStateEvent(
			t, "m.room.topic", "", alice, map[string]interface{}{"topic": "bananas"}, testutils.WithTimestamp(ts),
		),
	)

	ts = ts.Add(time.Second)
	r1Timeline = append(r1Timeline,
		testutils.NewStateEvent(
			t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join", "displayname": "all ice"}, testutils.WithTimestamp(ts),
		),
	)

	const room1ID = "!room1:localhost"
	r1 := roomEvents{
		roomID: room1ID,
		name:   "room 1",
		state:  r1CreateState,
		events: r1Timeline,
	}
	const room2ID = "!room2:localhost"
	r2 := roomEvents{
		roomID: room2ID,
		name:   "room 2",
		state:  r2CreateState,
		events: r2Timeline,
	}
	const room3ID = "!room3:localhost"
	r3 := roomEvents{
		roomID: room3ID,
		name:   "room 3",
		state:  r3CreateState,
		events: r3Timeline,
	}

	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	t.Log("We prepare to tell the proxy about these events.")
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(aliceToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(r1, r2, r3),
		},
	})

	t.Log("Alice requests a new sliding sync connection.")
	v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 20,
				},
				Ranges: sync3.SliceRanges{{0, 2}},
			},
		},
	})

	// Confirm that the poller was polled from.
	v2.waitUntilEmpty(t, aliceToken)

	t.Log("The proxy restarts.")
	v3.restart(t, v2, pqString)

	// Vary the bump event types, and compare the room order we get to what we expect
	cases := []struct {
		BumpEventTypes []string
		RoomIDs        []string
	}{
		{
			BumpEventTypes: []string{"m.room.message"},
			RoomIDs:        []string{room3ID, room2ID, room1ID},
		},
		{
			BumpEventTypes: []string{"m.room.topic"},
			RoomIDs:        []string{room2ID, room1ID, room3ID},
		},
		{
			BumpEventTypes: []string{},
			RoomIDs:        []string{room1ID, room2ID, room3ID},
		},
		{
			BumpEventTypes: []string{"m.room.topic", "m.room.message"},
			RoomIDs:        []string{room2ID, room3ID, room1ID},
		},
		{
			BumpEventTypes: []string{"m.room.member"},
			RoomIDs:        []string{room1ID, room3ID, room2ID},
		},
		{
			BumpEventTypes: []string{"com.example.doesnotexist"},
			// Fallback to join order
			RoomIDs: []string{room3ID, room2ID, room1ID},
		},
	}
	for _, testCase := range cases {
		t.Logf("Alice makes a new sync connection with bump events %v", testCase.BumpEventTypes)
		res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
			Lists: map[string]sync3.RequestList{
				"list": {
					Ranges:         sync3.SliceRanges{{0, 2}},
					BumpEventTypes: testCase.BumpEventTypes,
				},
			},
		})
		t.Logf("Alice should see the three rooms in the order %v", testCase.RoomIDs)
		m.MatchResponse(t, res, m.MatchList("list",
			m.MatchV3Ops(m.MatchV3SyncOp(0, 2, testCase.RoomIDs)),
			m.MatchV3Count(3),
		))
	}
}
