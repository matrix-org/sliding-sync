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

// Regression test for https://github.com/vector-im/element-x-ios-rageshakes/issues/314
// Caused by: the user cache getting corrupted and missing events, caused by it incorrectly replacing
// its timeline with an older one.
// To the end user, it manifests as missing messages in the timeline, because the proxy incorrectly
// said the events are C,F,G and not E,F,G.
func TestRoomSubscriptionMisorderedTimeline(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomState := createRoomState(t, alice, time.Now())
	abcInitialEvents := []json.RawMessage{
		testutils.NewMessageEvent(t, alice, "A"),
		testutils.NewMessageEvent(t, alice, "B"),
		testutils.NewMessageEvent(t, alice, "C"),
	}
	room := roomEvents{
		roomID: "!room:localhost",
		events: append(roomState, abcInitialEvents...),
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(room),
		},
	})
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"list": {
				Ranges:           sync3.SliceRanges{{0, 10}},
				RoomSubscription: sync3.RoomSubscription{TimelineLimit: 1},
			},
		},
	})
	// test that we get 1 event initally due to the timeline limit
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		room.roomID: {
			m.MatchRoomTimeline(abcInitialEvents[len(abcInitialEvents)-1:]),
		},
	}))

	// now live stream 2 events
	deLiveEvents := []json.RawMessage{
		testutils.NewMessageEvent(t, alice, "D"),
		testutils.NewMessageEvent(t, alice, "E"),
	}
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: room.roomID,
				events: deLiveEvents,
			}),
		},
	})
	v2.waitUntilEmpty(t, alice)

	// now add a room sub with timeline limit = 5, we will need to hit the DB to satisfy this.
	// We might destroy caches in a bad way. We might not return the most recent 5 events.
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			room.roomID: {
				TimelineLimit: 5,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		room.roomID: {
			// TODO: this is the correct result, but due to how timeline loading works currently
			// it will be returning the last 5 events BEFORE D,E, which isn't ideal but also isn't
			// incorrect per se due to the fact that clients don't know when D,E have been processed
			// on the server.
			// m.MatchRoomTimeline(append(abcInitialEvents, deLiveEvents...)),
			m.MatchRoomTimeline(append(roomState[len(roomState)-2:], abcInitialEvents...)),
		},
	}), m.LogResponse(t))

	// live stream the final 2 events
	fgLiveEvents := []json.RawMessage{
		testutils.NewMessageEvent(t, alice, "F"),
		testutils.NewMessageEvent(t, alice, "G"),
	}
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: room.roomID,
				events: fgLiveEvents,
			}),
		},
	})
	v2.waitUntilEmpty(t, alice)

	// now ask for timeline limit = 3, which may miss events if the caches got corrupted.
	// Do this on a fresh connection to force loadPos to update.
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			room.roomID: {
				TimelineLimit: 3,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		room.roomID: {
			m.MatchRoomTimeline(append(deLiveEvents[1:], fgLiveEvents...)),
		},
	}), m.LogResponse(t))
}
