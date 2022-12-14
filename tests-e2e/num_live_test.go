package syncv3_test

import (
	"testing"

	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils/m"
)

func TestNumLive(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)
	eventID := alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello World!!!!",
		},
	})

	// initial syncs -> no live events
	res := bob.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(
		map[string][]m.RoomMatcher{
			roomID: {
				MatchRoomTimelineContains(Event{ID: eventID}),
				m.MatchNumLive(0),
			},
		},
	))

	// live event -> 1 num live
	eventID2 := alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello World!!!! 2",
		},
	})
	res = bob.SlidingSyncUntilEventID(t, res.Pos, roomID, eventID2)
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(
		map[string][]m.RoomMatcher{
			roomID: {
				MatchRoomTimelineContains(Event{ID: eventID2}),
				m.MatchNumLive(1),
			},
		},
	))

	// from fresh => 0 num live
	res = bob.SlidingSyncUntilEventID(t, "", roomID, eventID2)
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(
		map[string][]m.RoomMatcher{
			roomID: {
				MatchRoomTimelineContains(Event{ID: eventID2}),
				m.MatchNumLive(0),
			},
		},
	))

	// now the big one -> 3 rooms, ask for top 2, bump 3rd room to top twice -> num_live=2
	roomID2 := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID2, nil)
	roomID3 := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID3, nil)
	// let the proxy become aware of these joins
	alice.SlidingSyncUntilMembership(t, "", roomID3, bob, "join")
	// at this point, roomID is at the bottom, check.
	res = bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{{0, 1}}, // top 2 rooms
				Sort:   []string{sync3.SortByRecency},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 2,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Ops(
		m.MatchV3SyncOp(0, 1, []string{roomID3, roomID2}),
	)))
	// now send 2 live events into roomID to bump it to the top
	eventID3 := alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "bump 1",
		},
	})
	eventID4 := alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "bump 2",
		},
	})
	// wait for the proxy to get it
	alice.SlidingSyncUntilEventID(t, "", roomID, eventID4)
	// now syncing with bob should see 2 live events
	res = bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{{0, 1}}, // top 2 rooms
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchNumLive(2),
			// TODO: should we be including event ID 2 given timeline limit is 2?
			MatchRoomTimeline([]Event{{ID: eventID2}, {ID: eventID3}, {ID: eventID4}}),
		},
	}))
}
