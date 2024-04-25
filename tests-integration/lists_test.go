package syncv3

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/sjson"
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
	const room1ID = "!room1:localhost"
	const room2ID = "!room2:localhost"
	const room3ID = "!room3:localhost"

	// Create three rooms, with a one-second pause between each creation.
	ts := time.Now()
	state := createRoomState(t, alice, ts)
	r2State := createRoomState(t, alice, ts.Add(time.Second))
	r3State := createRoomState(t, alice, ts.Add(2*time.Second))
	ts = ts.Add(2 * time.Second)

	r1Timeline := []json.RawMessage{}
	r2Timeline := []json.RawMessage{}
	r3Timeline := []json.RawMessage{}

	steps := []struct {
		timeline *[]json.RawMessage
		event    json.RawMessage
	}{
		{
			timeline: &r1Timeline,
			event:    testutils.NewStateEvent(t, "m.room.topic", "", alice, map[string]interface{}{"topic": "potato"}, testutils.WithTimestamp(ts)),
		},
		{
			timeline: &r1Timeline,
			event:    testutils.NewMessageEvent(t, alice, "message in room 1", testutils.WithTimestamp(ts)),
		},
		{
			timeline: &r2Timeline,
			event:    testutils.NewMessageEvent(t, alice, "message in room 2", testutils.WithTimestamp(ts)),
		},
		{
			timeline: &r3Timeline,
			event:    testutils.NewMessageEvent(t, alice, "message in room 3", testutils.WithTimestamp(ts)),
		},
		{
			timeline: &r2Timeline,
			event:    testutils.NewStateEvent(t, "m.room.topic", "", alice, map[string]interface{}{"topic": "bananas"}, testutils.WithTimestamp(ts)),
		},
		{
			timeline: &r1Timeline,
			event:    testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join", "displayname": "all ice"}, testutils.WithTimestamp(ts)),
		},
	}

	// Append events to the correct timeline. Add at least a second between
	// significant events, to ensure there aren't any timestamp clashes.
	for _, step := range steps {
		ts = ts.Add(time.Second)
		step.event = testutils.SetTimestamp(t, step.event, ts)
		*step.timeline = append(*step.timeline, step.event)
	}

	r1 := roomEvents{
		roomID: room1ID,
		name:   "room 1",
		state:  state,
		events: r1Timeline,
	}
	r2 := roomEvents{
		roomID: room2ID,
		name:   "room 2",
		state:  r2State,
		events: r2Timeline,
	}
	r3 := roomEvents{
		roomID: room3ID,
		name:   "room 3",
		state:  r3State,
		events: r3Timeline,
	}

	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	t.Log("Prepare to tell the proxy about three rooms and events in them.")
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

	// Confirm that the poller polled.
	v2.waitUntilEmpty(t, aliceToken)

	t.Log("The proxy restarts.")
	v3.restart(t, v2, pqString)

	// Vary the bump event types, and compare the room order we get to what we expect.
	// The pertinent events are:
	// (1) create and join r1
	// (2) create and join r2
	// (3) create and join r3
	// (4) r1: topic set
	// (5) r1: message
	// (6) r2: message
	// (7) r3: message
	// (8) r2: topic
	// (9) r1: profile change

	cases := []struct {
		BumpEventTypes []string
		RoomIDs        []string
	}{
		{
			BumpEventTypes: []string{"m.room.message"},
			// r3 message (7), r2 message (6), r1 message (5).
			RoomIDs: []string{room3ID, room2ID, room1ID},
		},
		{
			BumpEventTypes: []string{"m.room.topic"},
			// r2 topic (8), r1 topic (4), r3 join (3).
			RoomIDs: []string{room2ID, room1ID, room3ID},
		},
		{
			BumpEventTypes: []string{},
			// r1 profile (9), r2 topic (8), r3 message (7)
			RoomIDs: []string{room1ID, room2ID, room3ID},
		},
		{
			BumpEventTypes: []string{"m.room.topic", "m.room.message"},
			// r2 topic (8), r3 message (7), r1 message (5)
			RoomIDs: []string{room2ID, room3ID, room1ID},
		},
		{
			// r2 profile (8), r3 join (3), r1 join (1)
			BumpEventTypes: []string{"m.room.member"},
			RoomIDs:        []string{room1ID, room3ID, room2ID},
		},
		{
			BumpEventTypes: []string{"com.example.doesnotexist"},
			// r3 join (3), r2 join (2), r1 join (1)
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

func TestDeleteMSC4115Field(t *testing.T) {
	t.Run("stable prefix", func(t *testing.T) {
		testDeleteMSC4115Field(t, "membership")
	})
	t.Run("unstable prefix", func(t *testing.T) {
		testDeleteMSC4115Field(t, "io.element.msc4115.membership")
	})
}

func testDeleteMSC4115Field(t *testing.T, fieldName string) {
	rig := NewTestRig(t)
	defer rig.Finish()
	roomID := "!TestDeleteMSC4115Field:localhost"
	rig.SetupV2RoomsForUser(t, alice, NoFlush, map[string]RoomDescriptor{
		roomID: {},
	})
	aliceToken := rig.Token(alice)
	res := rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 10,
					RequiredState: [][2]string{{"m.room.name", "*"}},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		"a": {
			m.MatchV3Count(1),
		},
	}), m.MatchRoomSubscription(roomID))

	// ensure live events remove the field.
	liveEvent := testutils.NewMessageEvent(t, alice, "live event", testutils.WithUnsigned(map[string]interface{}{
		fieldName: "join",
	}))
	liveEventWithoutMembership := make(json.RawMessage, len(liveEvent))
	copy(liveEventWithoutMembership, liveEvent)
	liveEventWithoutMembership, err := sjson.DeleteBytes(liveEventWithoutMembership, "unsigned."+strings.ReplaceAll(fieldName, ".", `\.`))
	if err != nil {
		t.Fatalf("failed to delete unsigned.membership field")
	}
	rig.FlushEvent(t, alice, roomID, liveEvent)

	res = rig.V3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchRoomTimelineMostRecent(1, []json.RawMessage{liveEventWithoutMembership}),
		},
	}))

	// ensure state events remove the field.
	stateEvent := testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{
		"name": "Room Name",
	}, testutils.WithUnsigned(map[string]interface{}{
		fieldName: "join",
	}))
	stateEventWithoutMembership := make(json.RawMessage, len(stateEvent))
	copy(stateEventWithoutMembership, stateEvent)
	stateEventWithoutMembership, err = sjson.DeleteBytes(stateEventWithoutMembership, "unsigned."+strings.ReplaceAll(fieldName, ".", `\.`))
	if err != nil {
		t.Fatalf("failed to delete unsigned.membership field")
	}
	rig.V2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				state:  []json.RawMessage{stateEvent},
				events: []json.RawMessage{testutils.NewMessageEvent(t, alice, "dummy")},
			}),
		},
	})
	rig.V2.waitUntilEmpty(t, alice)

	// sending v2 state invalidates the SS connection so start again pre-emptively.
	res = rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 10,
					RequiredState: [][2]string{{"m.room.name", "*"}},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchRoomRequiredState([]json.RawMessage{stateEventWithoutMembership}),
		},
	}))
}
