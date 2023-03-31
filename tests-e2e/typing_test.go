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

	var syncResp *sync3.Response

	// Want at least one test of the initial sync behaviour (which hits `ProcessInitial`)
	// separate to the incremental sync behaviour (hits `AppendLive`)
	t.Run("Can limit by room in an initial sync", func(t *testing.T) {
		t.Log("Alice creates rooms 1 and 2. Bob joins both.")
		room1 := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 1"})
		room2 := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 2"})
		bob.JoinRoom(t, room1, nil)
		bob.JoinRoom(t, room2, nil)
		t.Logf("room1=%s room2=%s", room1, room2)

		t.Log("Bob types in rooms 1 and 2")
		bob.SendTyping(t, room1, true, 5000)
		bob.SendTyping(t, room2, true, 5000)

		t.Log("Alice makes an initial sync request, requesting typing notifications in room 2 only.")
		syncResp = alice.SlidingSync(t, sync3.Request{
			Extensions: extensions.Request{
				Typing: &extensions.TypingRequest{
					Core: extensions.Core{Enabled: &boolTrue, Lists: []string{}, Rooms: []string{room2}},
				},
			},
			Lists: map[string]sync3.RequestList{
				"window": {
					Ranges: sync3.SliceRanges{{0, 3}},
					Sort:   []string{sync3.SortByName},
				},
			},
		})

		// Note: no sentinel needed here: we have just done an initial v3 sync, so the
		// poller will make an initial v2 sync and see the typing EDUs.
		t.Log("Alice should see Bob typing in room 2 only.")
		m.MatchResponse(
			t,
			syncResp,
			m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
				room1: {},
				room2: {},
			}),
			m.MatchNotTyping(room1, []string{bob.UserID}),
			m.MatchTyping(room2, []string{bob.UserID}),
		)
	})

	t.Run("Can limit by list in an incremental sync", func(t *testing.T) {
		t.Log("Alice creates rooms 3 and 4. Bob joins both.")
		room3 := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 3"})
		room4 := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 4"})
		bob.JoinRoom(t, room3, nil)
		bob.JoinRoom(t, room4, nil)
		t.Logf("room3=%s room4=%s", room3, room4)

		t.Log("Bob types in rooms 3 and 4")
		bob.SendTyping(t, room3, true, 5000)
		bob.SendTyping(t, room4, true, 5000)

		t.Log("Alice incremental syncs until she sees Bob typing in room 4.")
		t.Log("She a window containing all rooms, and a narrower window containing last-named room only.")
		t.Log("She requests typing notifications in the narrow window only.")
		t.Log("She should not see Bob typing in room 3 at any point.")
		syncResp = alice.SlidingSyncUntil(
			t,
			syncResp.Pos,
			sync3.Request{
				Extensions: extensions.Request{
					Typing: &extensions.TypingRequest{
						Core: extensions.Core{Enabled: &boolTrue, Lists: []string{"window"}, Rooms: []string{}},
					},
				},
				Lists: map[string]sync3.RequestList{
					"window": {
						Ranges: sync3.SliceRanges{{3, 3}},
					},
					"all": {
						SlowGetAllRooms: &boolTrue,
					},
				}},
			func(response *sync3.Response) error {
				// Alice should never see Bob type in room 3.
				if m.MatchTyping(room3, []string{bob.UserID})(response) == nil {
					dump, _ := json.MarshalIndent(response, "", "    ")
					t.Fatalf("Alice saw Bob typing in room 3. Response was %s", dump)
				}

				// Alice wants to see Bob type in room 4.
				return m.MatchTyping(room4, []string{bob.UserID})(response)
			},
		)
	})
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
