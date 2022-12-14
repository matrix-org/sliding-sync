package syncv3_test

import (
	"testing"

	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils/m"
)

func TestLazyLoading(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	charlie := registerNewUser(t)
	sentinel := registerNewUser(t) // dummy user to ensure that the proxy has processed sent events

	// all 3 join the room and say something
	roomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	bob.JoinRoom(t, roomID, nil)
	charlie.JoinRoom(t, roomID, nil)
	sentinel.JoinRoom(t, roomID, nil)

	alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"body":    "Hello world",
			"msgtype": "m.text",
		},
	})

	bob.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"body":    "Hello world",
			"msgtype": "m.text",
		},
	})

	lastEventID := charlie.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"body":    "Hello world",
			"msgtype": "m.text",
		},
	})

	// alice requests the room lazy loaded with timeline limit 1 => sees only charlie
	res := alice.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
				RequiredState: [][2]string{
					{"m.room.member", "$LAZY"},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			MatchRoomRequiredState([]Event{
				{
					Type:     "m.room.member",
					StateKey: &charlie.UserID,
				},
			}),
			MatchRoomTimeline([]Event{
				{ID: lastEventID},
			}),
		},
	}))

	// bob requests the room lazy loaded with timeline limit 1 AND $ME => sees bob and charlie
	bobRes := bob.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
				RequiredState: [][2]string{
					{"m.room.member", "$LAZY"},
					{"m.room.member", "$ME"},
				},
			},
		},
	})
	m.MatchResponse(t, bobRes, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			MatchRoomRequiredState([]Event{
				{
					Type:     "m.room.member",
					StateKey: &charlie.UserID,
				},
				{
					Type:     "m.room.member",
					StateKey: &bob.UserID,
				},
			}),
			MatchRoomTimeline([]Event{
				{ID: lastEventID},
			}),
		},
	}))

	// charlie requests the room lazy loaded with $ME AND *,* with timeline limit 1 => sees himself but a bunch of other room state too
	charlieRes := charlie.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
				RequiredState: [][2]string{
					{"m.room.member", "$LAZY"},
					{"m.room.member", "$ME"},
					{"*", "*"},
				},
			},
		},
	})
	m.MatchResponse(t, charlieRes, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			MatchRoomRequiredState([]Event{
				{
					Type:     "m.room.member",
					StateKey: &charlie.UserID,
				},
				{
					Type:     "m.room.create",
					StateKey: ptr(""),
				},
				{
					Type:     "m.room.power_levels",
					StateKey: ptr(""),
				},
				{
					Type:     "m.room.history_visibility",
					StateKey: ptr(""),
				},
				{
					Type:     "m.room.join_rules",
					StateKey: ptr(""),
				},
			}),
			MatchRoomTimeline([]Event{
				{ID: lastEventID},
			}),
		},
	}))

	// alice now sends a message
	aliceEventID := alice.SendEventSynced(t, roomID, Event{Type: "m.room.message", Content: map[string]interface{}{"body": "hello", "msgtype": "m.text"}})
	sentinel.SlidingSyncUntilEventID(t, "", roomID, aliceEventID)

	// bob, who didn't previously get alice's m.room.member event, should now see this
	bobRes = bob.SlidingSync(t, sync3.Request{}, WithPos(bobRes.Pos))
	m.MatchResponse(t, bobRes, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			MatchRoomTimeline([]Event{{
				Type: "m.room.message",
				ID:   aliceEventID,
			}}),
			MatchRoomRequiredState([]Event{{
				Type:     "m.room.member",
				StateKey: &alice.UserID,
			}}),
		},
	}))

	// alice sends another message
	aliceEventID2 := alice.SendEventSynced(t, roomID, Event{Type: "m.room.message", Content: map[string]interface{}{"body": "hello2", "msgtype": "m.text"}})
	sentinel.SlidingSyncUntilEventID(t, "", roomID, aliceEventID2)

	// bob, who had just got alice's m.room.member event, shouldn't see it again.
	bobRes = bob.SlidingSync(t, sync3.Request{}, WithPos(bobRes.Pos))
	m.MatchResponse(t, bobRes, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			MatchRoomTimeline([]Event{{
				Type: "m.room.message",
				ID:   aliceEventID2,
			}}),
			MatchRoomRequiredState(nil),
		},
	}))

	// charlie does a sync, also gets alice's member event, exactly once
	charlieRes = charlie.SlidingSync(t, sync3.Request{}, WithPos(charlieRes.Pos))
	m.MatchResponse(t, charlieRes, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			MatchRoomTimeline([]Event{
				{
					Type: "m.room.message",
					ID:   aliceEventID,
				},
				{
					Type: "m.room.message",
					ID:   aliceEventID2,
				},
			}),
			MatchRoomRequiredState([]Event{{
				Type:     "m.room.member",
				StateKey: &alice.UserID,
			}}),
		},
	}))

}
