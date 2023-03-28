package syncv3_test

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/gjson"
)

func TestTyping(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)

	// typing requests are ignored on the initial sync as we only store typing notifs for _connected_ (polling)
	// users of which alice is not connected yet. Only live updates will show up. This is mainly to simplify
	// the proxy - server impls will be able to do this immediately.
	alice.SlidingSync(t, sync3.Request{}) // start polling
	bob.SlidingSync(t, sync3.Request{})

	bob.SendTyping(t, roomID, true, 5000)
	waitUntilTypingData(t, bob, roomID, []string{bob.UserID}) // ensure the proxy gets the data

	// make sure initial requests show typing
	res := alice.SlidingSync(t, sync3.Request{
		Extensions: extensions.Request{
			Typing: &extensions.TypingRequest{
				Core: extensions.Core{Enabled: &boolTrue},
			},
		},
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchTyping(roomID, []string{bob.UserID}))

	// make sure typing updates -> no typing go through
	bob.SendTyping(t, roomID, false, 5000)
	waitUntilTypingData(t, bob, roomID, []string{}) // ensure the proxy gets the data
	res = alice.SlidingSync(t, sync3.Request{}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchTyping(roomID, []string{}))

	// make sure typing updates -> start typing go through
	bob.SendTyping(t, roomID, true, 5000)
	waitUntilTypingData(t, bob, roomID, []string{bob.UserID}) // ensure the proxy gets the data
	res = alice.SlidingSync(t, sync3.Request{}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchTyping(roomID, []string{bob.UserID}))

	// make sure typing updates are consolidated when multiple people type
	alice.SendTyping(t, roomID, true, 5000)
	waitUntilTypingData(t, bob, roomID, []string{bob.UserID, alice.UserID}) // ensure the proxy gets the data
	res = alice.SlidingSync(t, sync3.Request{}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchTyping(roomID, []string{bob.UserID, alice.UserID}))

	// make sure if you type in a room not returned in the window it does not go through
	roomID2 := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID2, nil)
	res = alice.SlidingSyncUntilMembership(t, res.Pos, roomID2, bob, "join")
	bob.SendTyping(t, roomID2, true, 5000)
	waitUntilTypingData(t, bob, roomID2, []string{bob.UserID}) // ensure the proxy gets the data

	// alice should get this typing notif even if we aren't subscribing to it, because we do not track
	// the entire set of rooms the client is tracking, so it's entirely possible this room was returned
	// hours ago and the user wants to know information about it. We can't even rely on it being present
	// in the sliding window or direct subscriptions because clients sometimes spider the entire list of
	// rooms and then track "live" data. Typing is inherently live, so always return it.
	// TODO: parameterise this in the typing extension?
	res = alice.SlidingSync(t, sync3.Request{}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchTyping(roomID2, []string{bob.UserID}))

	// ensure that we only see 1x typing event and don't get dupes for the # connected users in the room
	alice.SendTyping(t, roomID, false, 5000)
	now := time.Now()
	numTypingEvents := 0
	for time.Since(now) < time.Second {
		res = alice.SlidingSync(t, sync3.Request{}, WithPos(res.Pos))
		if res.Extensions.Typing != nil && res.Extensions.Typing.Rooms != nil && res.Extensions.Typing.Rooms[roomID] != nil {
			typingEv := res.Extensions.Typing.Rooms[roomID]
			gotUserIDs := typingUsers(t, typingEv)
			// both alice and bob are typing in roomID, and we just sent a stop typing for alice, so only count
			// those events.
			if reflect.DeepEqual(gotUserIDs, []string{bob.UserID}) {
				numTypingEvents++
				t.Logf("typing ev: %v", string(res.Extensions.Typing.Rooms[roomID]))
			}
		}
	}
	if numTypingEvents > 1 {
		t.Errorf("got %d typing events, wanted 1", numTypingEvents)
	}
}

// Test that when you start typing without the typing extension, we don't return a no-op response.
func TestTypingNoUpdate(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)

	// typing requests are ignored on the initial sync as we only store typing notifs for _connected_ (polling)
	// users of which alice is not connected yet. Only live updates will show up. This is mainly to simplify
	// the proxy - server impls will be able to do this immediately.
	alice.SlidingSync(t, sync3.Request{}) // start polling
	res := bob.SlidingSync(t, sync3.Request{
		// no typing extension
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 0,
			},
		},
	})
	alice.SendTyping(t, roomID, true, 5000)
	waitUntilTypingData(t, alice, roomID, []string{alice.UserID}) // wait until alice is typing
	// bob should not return early with an empty roomID response
	res = bob.SlidingSync(t, sync3.Request{}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(nil))
}

// Test that members that have not yet been lazy loaded get lazy loaded when they are sending typing events
func TestTypingLazyLoad(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)

	alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"body":    "hello world!",
			"msgtype": "m.text",
		},
	})
	alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"body":    "hello world!",
			"msgtype": "m.text",
		},
	})

	// Initial sync request with lazy loading and typing enabled
	syncResp := alice.SlidingSync(t, sync3.Request{
		Extensions: extensions.Request{
			Typing: &extensions.TypingRequest{
				Core: extensions.Core{Enabled: &boolTrue},
			},
		},
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

	// There should only be Alice lazy loaded
	m.MatchResponse(t, syncResp, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			MatchRoomRequiredState([]Event{{Type: "m.room.member", StateKey: &alice.UserID}}),
		},
	}))

	// Bob starts typing
	bob.SendTyping(t, roomID, true, 5000)

	// Alice should now see Bob typing and Bob should be lazy loaded
	syncResp = waitUntilTypingData(t, alice, roomID, []string{bob.UserID})
	m.MatchResponse(t, syncResp, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			MatchRoomRequiredState([]Event{{Type: "m.room.member", StateKey: &bob.UserID}}),
		},
	}))
}

func TestTypingRespectsExtensionScope(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	t.Log("Alice creates two rooms. Bob joins both.")
	room1 := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	room2 := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	bob.JoinRoom(t, room1, nil)
	bob.JoinRoom(t, room2, nil)

	t.Log("Bob types in both rooms")
	bob.SendTyping(t, room1, true, 5000)
	bob.SendTyping(t, room2, true, 5000)

	t.Log("Alice makes an initial sync request, opting out of all typing notifications")
	syncResp := alice.SlidingSync(t, sync3.Request{
		Extensions: extensions.Request{
			Typing: &extensions.TypingRequest{
				Core: extensions.Core{Enabled: &boolTrue, Lists: []string{}, Rooms: []string{}},
			},
		},
		Lists: map[string]sync3.RequestList{
			"window": {
				Ranges: sync3.SliceRanges{{0, 1}},
			},
		},
	})

	t.Log("Alice should see no indication that Bob is typing.")
	m.MatchResponse(
		t,
		syncResp,
		m.MatchList(
			"window",
			m.MatchV3Count(2),
			m.MatchV3Ops(
				m.MatchV3SyncOp(0, 1, []string{room1, room2}, true),
			),
		),
		m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
			room1: {},
			room2: {},
		}),
		m.MatchNotTyping(room1, []string{bob.UserID}),
		m.MatchNotTyping(room2, []string{bob.UserID}),
	)

	t.Log("Alice sends a message in room 1")
	messageID1 := alice.SendEventSynced(t, room1, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"body":    "hello room 1!",
			"msgtype": "m.text",
		},
	})

	t.Log("Alice syncs until she sees messageID1, requesting future typing notifications in room 1 only.")
	syncResp = alice.SlidingSyncUntilEvent(
		t,
		syncResp.Pos,
		sync3.Request{
			Extensions: extensions.Request{
				Typing: &extensions.TypingRequest{
					Core: extensions.Core{Rooms: []string{room1}},
				},
			},
		},
		room1,
		Event{ID: messageID1},
	)

	t.Log("Bob types in room1")
	bob.SendTyping(t, room1, true, 5000)

	t.Log("Alice should be able to see that Bob is typing in room 1.")
	syncResp = alice.SlidingSyncUntil(t, syncResp.Pos, sync3.Request{}, m.MatchTyping(room1, []string{bob.UserID}))

	t.Log("Bob types in room2")
	bob.SendTyping(t, room2, true, 5000)

	t.Log("Alice sends a message in room 2")
	messageID2 := alice.SendEventSynced(t, room2, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"body":    "hello room 2!",
			"msgtype": "m.text",
		},
	})

	t.Log("Alice syncs until she sees her messages.")
	syncResp = alice.SlidingSyncUntilEventID(
		t,
		syncResp.Pos,
		room2,
		messageID2,
	)

	t.Log("Alice should not see Bob typing in room 2.")
	m.MatchResponse(
		t,
		syncResp,
		m.MatchNotTyping(room2, []string{bob.UserID}),
	)
}

func waitUntilTypingData(t *testing.T, client *CSAPI, roomID string, wantUserIDs []string) *sync3.Response {
	t.Helper()
	sort.Strings(wantUserIDs)
	return client.SlidingSyncUntil(t, "", sync3.Request{
		Extensions: extensions.Request{
			Typing: &extensions.TypingRequest{
				Core: extensions.Core{Enabled: &boolTrue},
			},
		},
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
				RequiredState: [][2]string{
					{"m.room.member", "$LAZY"},
					{"m.room.member", "$ME"},
				},
			},
		},
	},
		m.MatchTyping(roomID, wantUserIDs),
	)
}

func typingUsers(t *testing.T, ev json.RawMessage) []string {
	userIDs := gjson.ParseBytes(ev).Get("content.user_ids").Array()
	gotUserIDs := make([]string, len(userIDs))
	for i := range userIDs {
		gotUserIDs[i] = userIDs[i].Str
	}
	sort.Strings(gotUserIDs)
	return gotUserIDs
}
