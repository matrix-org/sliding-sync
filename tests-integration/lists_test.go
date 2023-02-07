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
	v2.addAccount(alice, aliceToken)
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
		m.MatchV3SyncOp(0, 5, []string{roomA, roomB, roomC}),
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
