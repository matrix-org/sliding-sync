package syncv3_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/gjson"
)

// tests that if we upgrade a room it is removed from the list. If we request old rooms it should be included.
func TestIncludeOldRooms(t *testing.T) {
	client := registerNewUser(t)
	roomID := client.CreateRoom(t, map[string]interface{}{})

	res := client.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: [][2]int64{{0, 1}},
				RoomSubscription: sync3.RoomSubscription{
					RequiredState: [][2]string{{"m.room.create", ""}},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{roomID}),
	)))
	newRoomID := upgradeRoom(t, client, roomID)
	t.Logf("old %s new %s", roomID, newRoomID)
	time.Sleep(100 * time.Millisecond) // let the proxy process it

	res = client.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: [][2]int64{{0, 1}},
			},
		},
	}, WithPos(res.Pos))
	var tombstoneEventID string
	// count is 1 as we are auto-joined to the upgraded room
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3DeleteOp(1),
		m.MatchV3InsertOp(0, newRoomID), // insert new room
		m.MatchV3DeleteOp(1),            // remove old room
	)), m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchRoomInitial(false),
			func(r sync3.Room) error {
				// last timeline event should be the tombstone
				lastEvent := r.Timeline[len(r.Timeline)-1]
				ev := gjson.ParseBytes(lastEvent)
				if ev.Get("type").Str != "m.room.tombstone" || !ev.Get("state_key").Exists() || ev.Get("state_key").Str != "" {
					return fmt.Errorf("last event wasn't a tombstone event: %v", string(lastEvent))
				}
				tombstoneEventID = ev.Get("event_id").Str
				return nil
			},
		},
		// 2nd MatchRoomSubscriptions so we can pull out the event ID from the old room
	}), m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
		newRoomID: {
			m.MatchRoomInitial(true),
			m.MatchJoinCount(1),
			func(r sync3.Room) error { // nest it so the previous matcher has time to set tombstoneEventID
				return MatchRoomRequiredStateStrict([]Event{
					{
						Type:     "m.room.create",
						StateKey: ptr(""),
						Content: map[string]interface{}{
							"room_version": "9",
							"predecessor": map[string]interface{}{
								"room_id":  roomID,
								"event_id": tombstoneEventID,
							},
							"creator": client.UserID,
						},
					},
				})(r)
			},
		},
	}))

	// now fresh sync with old rooms enabled
	res = client.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: [][2]int64{{0, 2}},
				RoomSubscription: sync3.RoomSubscription{
					RequiredState: [][2]string{{"m.room.member", client.UserID}},
					IncludeOldRooms: &sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.create", ""}, {"m.room.tombstone", ""}},
					},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{newRoomID}),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		newRoomID: {
			MatchRoomRequiredStateStrict([]Event{
				{
					Type:     "m.room.member",
					StateKey: &client.UserID,
				},
			}),
		},
		roomID: {
			MatchRoomRequiredStateStrict([]Event{
				{
					Type:     "m.room.create",
					StateKey: ptr(""),
				},
				{
					Type:     "m.room.tombstone",
					StateKey: ptr(""),
				},
			}),
		},
	}))

	// finally, a fresh sync without include_old_rooms -> newest room only
	// now fresh sync with old rooms enabled
	res = client.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: [][2]int64{{0, 2}},
				RoomSubscription: sync3.RoomSubscription{
					RequiredState: [][2]string{{"m.room.member", client.UserID}},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{newRoomID}),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		newRoomID: {
			MatchRoomRequiredStateStrict([]Event{
				{
					Type:     "m.room.member",
					StateKey: &client.UserID,
				},
			}),
		},
	}))
}

// make a long upgrade chain of A -> B -> C -> D and then make sure that we can:
// - explicitly subscribe to old rooms e.g B
// - in that subscription, include old rooms to return A and nothing else.
// - check that if you leave a room e.g B, it breaks the chain when requesting old rooms (no A)
func TestIncludeOldRoomsLongChain(t *testing.T) {
	client := registerNewUser(t)
	// seed the server with this client, we need to do this so the proxy has timeline history to
	// return so we can assert events appear in the right rooms
	res := client.SlidingSync(t, sync3.Request{})
	roomA := client.CreateRoom(t, map[string]interface{}{})
	client.SendEventSynced(t, roomA, Event{
		Type:    "m.room.message",
		Content: map[string]interface{}{"body": "A", "msgtype": "m.text"},
	})
	roomB := upgradeRoom(t, client, roomA)
	eventB := client.SendEventSynced(t, roomB, Event{
		Type:    "m.room.message",
		Content: map[string]interface{}{"body": "B", "msgtype": "m.text"},
	})
	roomC := upgradeRoom(t, client, roomB)
	client.SendEventSynced(t, roomC, Event{
		Type:    "m.room.message",
		Content: map[string]interface{}{"body": "C", "msgtype": "m.text"},
	})
	roomD := upgradeRoom(t, client, roomC)
	eventD := client.SendEventSynced(t, roomD, Event{
		Type:    "m.room.message",
		Content: map[string]interface{}{"body": "D", "msgtype": "m.text"},
	})
	t.Logf("A:%s B:%s C:%s D:%s", roomA, roomB, roomC, roomD)
	// wait until we have seen the final event and final upgrade
	client.SlidingSyncUntilEventID(t, "", roomD, eventD)
	client.SlidingSyncUntilEvent(t, "", sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomC: {
				TimelineLimit: 5,
			},
		},
	}, roomC, Event{Type: "m.room.tombstone", StateKey: ptr("")})

	// can we subscribe to old rooms? Room B
	res = client.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomB: {
				RequiredState: [][2]string{{"m.room.member", client.UserID}},
				TimelineLimit: 4, // tombstone event and msg
				IncludeOldRooms: &sync3.RoomSubscription{
					RequiredState: [][2]string{{"m.room.create", ""}},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomA: {
			MatchRoomRequiredStateStrict([]Event{
				{
					Type:     "m.room.create",
					StateKey: ptr(""),
				},
			}),
		},
		roomB: {
			MatchRoomRequiredStateStrict([]Event{
				{
					Type:     "m.room.member",
					StateKey: &client.UserID,
				},
			}),
			MatchRoomTimelineContains(Event{
				ID: eventB,
			}),
		},
	}))

	// now leave room B and try the chain from D, we shouldn't see B or A
	client.LeaveRoom(t, roomB)
	client.SlidingSyncUntilEvent(t, res.Pos, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomB: {
				TimelineLimit: 5,
			},
		},
	}, roomB, Event{Type: "m.room.member", StateKey: &client.UserID, Content: map[string]interface{}{"membership": "leave"}})

	res = client.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomD: {
				RequiredState: [][2]string{{"m.room.member", client.UserID}},
				IncludeOldRooms: &sync3.RoomSubscription{
					RequiredState: [][2]string{{"m.room.create", ""}},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomC: {
			MatchRoomRequiredStateStrict([]Event{
				{
					Type:     "m.room.create",
					StateKey: ptr(""),
				},
			}),
		},
		roomD: {
			MatchRoomRequiredStateStrict([]Event{
				{
					Type:     "m.room.member",
					StateKey: &client.UserID,
				},
			}),
		},
	}))
}

// test that if you have a list version and direct sub version of include_old_rooms, they get unioned correctly.
func TestIncludeOldRoomsSubscriptionUnion(t *testing.T) {
	client := registerNewUser(t)
	roomA := client.CreateRoom(t, map[string]interface{}{})
	roomB := upgradeRoom(t, client, roomA)

	// should union to timeline_limit=2, req_state=create+member+tombstone
	res := client.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: [][2]int64{{0, 1}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 0,
					IncludeOldRooms: &sync3.RoomSubscription{
						TimelineLimit: 0,
						RequiredState: [][2]string{{"m.room.member", client.UserID}, {"m.room.create", ""}},
					},
				},
			},
		},
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomB: {
				TimelineLimit: 0,
				IncludeOldRooms: &sync3.RoomSubscription{
					TimelineLimit: 1,
					RequiredState: [][2]string{{"m.room.tombstone", ""}, {"m.room.create", ""}},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{roomB}),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomA: {
			MatchRoomRequiredStateStrict([]Event{
				{
					Type: "m.room.create", StateKey: ptr(""),
				},
				{
					Type: "m.room.member", StateKey: &client.UserID,
				},
				{
					Type: "m.room.tombstone", StateKey: ptr(""),
				},
			}),
			func(r sync3.Room) error {
				if len(r.Timeline) != 1 {
					return fmt.Errorf("timeline length %d want 1", len(r.Timeline))
				}
				return nil
			},
		},
		roomB: {
			MatchRoomRequiredStateStrict(nil),
			MatchRoomTimeline(nil),
		},
	}))
}

func upgradeRoom(t *testing.T, client *CSAPI, roomID string) (newRoomID string) {
	upgradeRes := client.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "rooms", roomID, "upgrade"}, WithJSONBody(t, map[string]interface{}{
		"new_version": "9",
	}))
	var body map[string]interface{}
	if err := json.NewDecoder(upgradeRes.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %s", err)
	}
	newRoomID = body["replacement_room"].(string)
	return newRoomID
}
