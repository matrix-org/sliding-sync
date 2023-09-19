package syncv3_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/gjson"
)

func TestRoomStateTransitions(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	// make 4 rooms and set bob's membership state in each to a different value.
	joinRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	bob.JoinRoom(t, joinRoomID, nil)
	kickRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	bob.JoinRoom(t, kickRoomID, nil)
	alice.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "rooms", kickRoomID, "kick"}, WithJSONBody(t, map[string]interface{}{
		"user_id": bob.UserID,
	}))
	banRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	bob.JoinRoom(t, banRoomID, nil)
	alice.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "rooms", banRoomID, "ban"}, WithJSONBody(t, map[string]interface{}{
		"user_id": bob.UserID,
	}))
	inviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	alice.InviteRoom(t, inviteRoomID, bob.UserID)

	// seed the proxy with Alice data
	alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
				Sort: []string{sync3.SortByRecency},
			},
		},
	})

	// bob should see the invited/joined rooms
	bobRes := bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
				Sort: []string{sync3.SortByRecency},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
					RequiredState: [][2]string{
						{"m.room.create", ""},
					},
				},
			},
		},
	})
	m.MatchResponse(t, bobRes, m.MatchList("a", m.MatchV3Count(2), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 1, []string{inviteRoomID, joinRoomID}),
	)), m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
		inviteRoomID: {
			m.MatchRoomHighlightCount(1),
			m.MatchRoomInitial(true),
			m.MatchRoomRequiredState(nil),
			m.MatchInviteCount(1),
			m.MatchJoinCount(1),
			MatchRoomInviteState([]Event{
				{
					Type:     "m.room.create",
					StateKey: ptr(""),
					// no content as it includes the room version which we don't want to guess/hardcode
				},
				{
					Type:     "m.room.join_rules",
					StateKey: ptr(""),
					Content: map[string]interface{}{
						"join_rule": "public",
					},
				},
			}, true),
		},
		joinRoomID: {},
	}),
	)

	// now bob accepts the invite
	bob.JoinRoom(t, inviteRoomID, nil)

	// wait until the proxy has got this data
	alice.SlidingSyncUntilMembership(t, "", inviteRoomID, bob, "join")

	// the room should be updated with the initial flag set to replace what was in the invite state
	bobRes = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
			},
		},
	}, WithPos(bobRes.Pos))
	m.MatchResponse(t, bobRes, m.MatchNoV3Ops(), m.MatchList("a", m.MatchV3Count(2)), m.MatchRoomSubscription(inviteRoomID,
		MatchRoomRequiredStateStrict([]Event{
			{
				Type:     "m.room.create",
				StateKey: ptr(""),
			},
		}),
		MatchRoomTimelineMostRecent(1, []Event{
			{
				Type:     "m.room.member",
				StateKey: ptr(bob.UserID),
				Content: map[string]interface{}{
					"membership":  "join",
					"displayname": bob.Localpart,
				},
				Sender: bob.UserID,
			},
		}),
		m.MatchRoomInitial(true),
		m.MatchJoinCount(2),
		m.MatchInviteCount(0),
		m.MatchRoomHighlightCount(0),
	))
}

func TestInviteRejection(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	// ensure that invite state correctly propagates. One room will already be in 'invite' state
	// prior to the first proxy sync, whereas the 2nd will transition.
	firstInviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": "First"})
	alice.InviteRoom(t, firstInviteRoomID, bob.UserID)
	secondInviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": "Second"})
	t.Logf("TestInviteRejection first %s second %s", firstInviteRoomID, secondInviteRoomID)

	// sync as bob, we should see 1 invite
	res := bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{firstInviteRoomID}),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		firstInviteRoomID: {
			m.MatchRoomInitial(true),
			m.MatchInviteCount(1),
			m.MatchJoinCount(1),
			MatchRoomInviteState([]Event{
				{
					Type:     "m.room.name",
					StateKey: ptr(""),
					Content: map[string]interface{}{
						"name": "First",
					},
				},
			}, true),
		},
	}))

	_, since := bob.MustSync(t, SyncReq{})
	// now invite bob
	alice.InviteRoom(t, secondInviteRoomID, bob.UserID)
	since = bob.MustSyncUntil(t, SyncReq{Since: since}, SyncInvitedTo(bob.UserID, secondInviteRoomID))

	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(2), m.MatchV3Ops(
		m.MatchV3DeleteOp(1),
		m.MatchV3InsertOp(0, secondInviteRoomID),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		secondInviteRoomID: {
			m.MatchRoomInitial(true),
			MatchRoomInviteState([]Event{
				{
					Type:     "m.room.name",
					StateKey: ptr(""),
					Content: map[string]interface{}{
						"name": "Second",
					},
				},
			}, true),
		},
	}))

	// now reject the invites

	bob.LeaveRoom(t, firstInviteRoomID)
	bob.LeaveRoom(t, secondInviteRoomID)
	bob.MustSyncUntil(t, SyncReq{Since: since}, SyncLeftFrom(bob.UserID, secondInviteRoomID))
	// TODO: proxy needs to have processed this event enough for it to be waiting for us
	time.Sleep(100 * time.Millisecond)

	// the list should be purged
	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(0), m.MatchV3Ops(
		m.MatchV3DeleteOp(1),
		m.MatchV3DeleteOp(0),
	)))

	// fresh sync -> no invites
	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(nil), m.MatchList("a", m.MatchV3Count(0)))
}

func TestInviteAcceptance(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	bob := registerNamedUser(t, "bob")

	// ensure that invite state correctly propagates. One room will already be in 'invite' state
	// prior to the first proxy sync, whereas the 2nd will transition.
	t.Logf("Alice creates two rooms and invites Bob to the first.")
	firstInviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": "First"})
	alice.InviteRoom(t, firstInviteRoomID, bob.UserID)
	secondInviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": "Second"})
	t.Logf("first %s second %s", firstInviteRoomID, secondInviteRoomID)

	t.Log("Sync as Bob, requesting invites only. He should see 1 invite")
	res := bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{firstInviteRoomID}),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		firstInviteRoomID: {
			m.MatchRoomInitial(true),
			m.MatchInviteCount(1),
			m.MatchJoinCount(1),
			MatchRoomInviteState([]Event{
				{
					Type:     "m.room.name",
					StateKey: ptr(""),
					Content: map[string]interface{}{
						"name": "First",
					},
				},
			}, true),
		},
	}))

	t.Log("Alice invites bob to room 2.")
	alice.InviteRoom(t, secondInviteRoomID, bob.UserID)
	t.Log("Alice syncs until she sees Bob's invite.")
	alice.SlidingSyncUntilMembership(t, "", secondInviteRoomID, bob, "invite")

	t.Log("Bob syncs. He should see the invite to room 2 as well.")
	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(2), m.MatchV3Ops(
		m.MatchV3DeleteOp(1),
		m.MatchV3InsertOp(0, secondInviteRoomID),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		secondInviteRoomID: {
			m.MatchRoomInitial(true),
			m.MatchInviteCount(1),
			m.MatchJoinCount(1),
			MatchRoomInviteState([]Event{
				{
					Type:     "m.room.name",
					StateKey: ptr(""),
					Content: map[string]interface{}{
						"name": "Second",
					},
				},
			}, true),
		},
	}))

	t.Log("Bob accept the invites.")
	bob.JoinRoom(t, firstInviteRoomID, nil)
	bob.JoinRoom(t, secondInviteRoomID, nil)

	t.Log("Alice syncs until she sees Bob join room 1.")
	alice.SlidingSyncUntilMembership(t, "", firstInviteRoomID, bob, "join")
	t.Log("Alice syncs until she sees Bob join room 2.")
	alice.SlidingSyncUntilMembership(t, "", secondInviteRoomID, bob, "join")

	t.Log("Bob does an incremental sync")
	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	t.Log("Both of his invites should be purged.")
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(0), m.MatchV3Ops(
		m.MatchV3DeleteOp(1),
		m.MatchV3DeleteOp(0),
	)))

	t.Log("Bob makes a fresh sliding sync request.")
	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	t.Log("He should see no invites.")
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(nil), m.MatchList("a", m.MatchV3Count(0)))
}

// Regression test for https://github.com/matrix-org/sliding-sync/issues/66
// whereby the invite fails to appear to clients when you invite->reject->invite
func TestInviteRejectionTwice(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomName := "It's-a-me-invitio"
	inviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": roomName})
	t.Logf("TestInviteRejectionTwice room %s", inviteRoomID)

	// sync as bob, we see no invites yet.
	res := bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(0)), m.MatchRoomSubscriptionsStrict(nil))

	// now invite bob
	alice.InviteRoom(t, inviteRoomID, bob.UserID)
	// sync as bob until we see the room (we aren't interested in this invite)
	res = bob.SlidingSyncUntilMembership(t, res.Pos, inviteRoomID, bob, "invite")
	t.Logf("bob is invited")
	// reject the invite and sync until we see it disappear (we aren't interested in this rejection either!)
	bob.LeaveRoom(t, inviteRoomID)
	res = bob.SlidingSyncUntilMembership(t, res.Pos, inviteRoomID, bob, "leave")

	// now invite bob again, we should see this (the regression was that we didn't until we initial synced!)
	alice.InviteRoom(t, inviteRoomID, bob.UserID)
	bob.SlidingSyncUntil(t, res.Pos, sync3.Request{}, func(r *sync3.Response) error {
		return m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
			inviteRoomID: {
				m.LogRoom(t),
				m.MatchInviteCount(1),
				MatchRoomInviteState([]Event{
					{
						Type:     "m.room.name",
						StateKey: ptr(""),
						Content: map[string]interface{}{
							"name": roomName,
						},
					},
				}, true),
			},
		})(r)
	})
}

func TestLeavingRoomReturnsOneEvent(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomName := "It's-a-me-invitio"

	for _, aliceSyncing := range []bool{false, true} {
		t.Run(fmt.Sprintf("leaving a room returns one leave event (multiple poller=%v)", aliceSyncing), func(t *testing.T) {
			inviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": roomName})
			t.Logf("TestLeavingRoomReturnsOneEvent room %s", inviteRoomID)

			if aliceSyncing {
				alice.SlidingSync(t, sync3.Request{})
			}

			// sync as bob, we see no invites yet.
			res := bob.SlidingSync(t, sync3.Request{
				Lists: map[string]sync3.RequestList{
					"a": {
						Ranges: sync3.SliceRanges{{0, 20}},
						Filters: &sync3.RequestFilters{
							IsInvite: &boolTrue,
						},
					},
				},
			})
			m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(0)), m.MatchRoomSubscriptionsStrict(nil))

			// now invite bob
			alice.InviteRoom(t, inviteRoomID, bob.UserID)
			// sync as bob until we see the room
			res = bob.SlidingSyncUntilMembership(t, res.Pos, inviteRoomID, bob, "invite")
			t.Logf("bob is invited")

			// join the room
			bob.JoinRoom(t, inviteRoomID, []string{})
			res = bob.SlidingSyncUntilMembership(t, res.Pos, inviteRoomID, bob, "join")
			t.Logf("bob joined")

			// leave the room again, we should receive exactly one leave response
			bob.LeaveRoom(t, inviteRoomID)
			res = bob.SlidingSyncUntilMembership(t, res.Pos, inviteRoomID, bob, "leave")

			if room, ok := res.Rooms[inviteRoomID]; ok {
				// If alice is NOT syncing, we run into this failure mode
				if c := len(room.Timeline); c > 1 {
					for _, ev := range res.Rooms[inviteRoomID].Timeline {
						t.Logf("[multiple poller=%v] Event: %s", aliceSyncing, ev)
					}
					t.Errorf("[multiple poller=%v] expected 1 timeline event, got %d", aliceSyncing, c)
				}
			} else {
				t.Errorf("[multiple poller=%v] expected room %s in response, but didn't find it", aliceSyncing, inviteRoomID)
			}

			res = bob.SlidingSync(t, sync3.Request{}, WithPos(res.Pos))

			// this should not happen, as we already send down the leave event
			// If alice is syncing, we run into this failure mode
			if room, ok := res.Rooms[inviteRoomID]; ok {
				for _, ev := range room.Timeline {
					t.Logf("[multiple poller=%v] Event: %s", aliceSyncing, ev)
				}
				t.Errorf("[multiple poller=%v] expected room not to be in response", aliceSyncing)
			}
		})
	}
}

// Basically the same as above, without joining the room (separate test to make sure
// we don't already have a poller running for Alice)
func TestRejectingInviteReturnsOneEvent(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomName := "It's-a-me-invitio"

	for _, aliceSyncing := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejecting an invite returns one leave event (multiple poller=%v)", aliceSyncing), func(t *testing.T) {
			inviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": roomName})
			t.Logf("TestRejectingInviteReturnsOneEvent room %s", inviteRoomID)

			if aliceSyncing {
				alice.SlidingSync(t, sync3.Request{})
			}

			// sync as bob, we see no invites yet.
			res := bob.SlidingSync(t, sync3.Request{
				Lists: map[string]sync3.RequestList{
					"a": {
						Ranges: sync3.SliceRanges{{0, 20}},
						Filters: &sync3.RequestFilters{
							IsInvite: &boolTrue,
						},
					},
				},
			})
			m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(0)), m.MatchRoomSubscriptionsStrict(nil))

			// now invite bob
			alice.InviteRoom(t, inviteRoomID, bob.UserID)
			// sync as bob until we see the room
			res = bob.SlidingSyncUntilMembership(t, res.Pos, inviteRoomID, bob, "invite")
			t.Logf("bob is invited")

			// reject the invite, we should receive exactly one leave response
			bob.LeaveRoom(t, inviteRoomID)
			res = bob.SlidingSyncUntilMembership(t, res.Pos, inviteRoomID, bob, "leave")
			t.Logf("bob rejected the invite")

			if room, ok := res.Rooms[inviteRoomID]; ok {
				// If alice is NOT syncing, we run into this failure mode
				if c := len(room.Timeline); c > 1 {
					for _, ev := range res.Rooms[inviteRoomID].Timeline {
						t.Logf("[multiple poller=%v] Event: %s", aliceSyncing, ev)
					}
					t.Errorf("[multiple poller=%v] expected 1 timeline event, got %d", aliceSyncing, c)
				}
			} else {
				t.Errorf("[multiple poller=%v] expected room %s in response, but didn't find it", aliceSyncing, inviteRoomID)
			}

			res = bob.SlidingSync(t, sync3.Request{}, WithPos(res.Pos))

			// this should not happen, as we already send down the leave event
			// If alice is syncing, we run into this failure mode
			if room, ok := res.Rooms[inviteRoomID]; ok {
				for _, ev := range room.Timeline {
					t.Logf("[multiple poller=%v] Event: %s", aliceSyncing, ev)
				}
				t.Errorf("[multiple poller=%v] expected room not to be in response", aliceSyncing)
			}
		})
	}
}

// Test to check that room heroes are returned if the membership changes
func TestHeroesOnMembershipChanges(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	charlie := registerNewUser(t)

	t.Run("nameless room uses heroes to calculate roomname", func(t *testing.T) {
		// create a room without a name, to ensure we calculate the room name based on
		// room heroes
		roomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})

		bob.JoinRoom(t, roomID, []string{})

		res := alice.SlidingSyncUntilMembership(t, "", roomID, bob, "join")
		// we expect to see Bob as a hero
		if c := len(res.Rooms[roomID].Heroes); c > 1 {
			t.Errorf("expected 1 room hero, got %d", c)
		}
		if gotUserID := res.Rooms[roomID].Heroes[0].ID; gotUserID != bob.UserID {
			t.Errorf("expected userID %q, got %q", gotUserID, bob.UserID)
		}

		// Now join with Charlie, the heroes and the room name should change
		charlie.JoinRoom(t, roomID, []string{})
		res = alice.SlidingSyncUntilMembership(t, res.Pos, roomID, charlie, "join")

		// we expect to see Bob as a hero
		if c := len(res.Rooms[roomID].Heroes); c > 2 {
			t.Errorf("expected 2 room hero, got %d", c)
		}
		if gotUserID := res.Rooms[roomID].Heroes[0].ID; gotUserID != bob.UserID {
			t.Errorf("expected userID %q, got %q", gotUserID, bob.UserID)
		}
		if gotUserID := res.Rooms[roomID].Heroes[1].ID; gotUserID != charlie.UserID {
			t.Errorf("expected userID %q, got %q", gotUserID, charlie.UserID)
		}

		// Send a message, the heroes shouldn't change
		msgEv := bob.SendEventSynced(t, roomID, Event{
			Type:    "m.room.roomID",
			Content: map[string]interface{}{"body": "Hello world", "msgtype": "m.text"},
		})

		res = alice.SlidingSyncUntilEventID(t, res.Pos, roomID, msgEv)
		if len(res.Rooms[roomID].Heroes) > 0 {
			t.Errorf("expected no change to room heros")
		}

		// Now leave with Charlie, only Bob should be in the heroes list
		charlie.LeaveRoom(t, roomID)
		res = alice.SlidingSyncUntilMembership(t, res.Pos, roomID, charlie, "leave")
		if c := len(res.Rooms[roomID].Heroes); c > 1 {
			t.Errorf("expected 1 room hero, got %d", c)
		}
		if gotUserID := res.Rooms[roomID].Heroes[0].ID; gotUserID != bob.UserID {
			t.Errorf("expected userID %q, got %q", gotUserID, bob.UserID)
		}
	})

	t.Run("named rooms don't have heroes", func(t *testing.T) {
		namedRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "my room without heroes"})
		// this makes sure that even if bob is joined, we don't return any heroes
		bob.JoinRoom(t, namedRoomID, []string{})

		res := alice.SlidingSyncUntilMembership(t, "", namedRoomID, bob, "join")
		if len(res.Rooms[namedRoomID].Heroes) > 0 {
			t.Errorf("expected no heroes, got %#v", res.Rooms[namedRoomID].Heroes)
		}
	})

	t.Run("rooms with aliases don't have heroes", func(t *testing.T) {
		aliasRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})

		alias := fmt.Sprintf("#%s-%d:%s", t.Name(), time.Now().Unix(), alice.Domain)
		alice.MustDoFunc(t, "PUT", []string{"_matrix", "client", "v3", "directory", "room", alias},
			WithJSONBody(t, map[string]any{"room_id": aliasRoomID}),
		)
		alice.SetState(t, aliasRoomID, "m.room.canonical_alias", "", map[string]any{
			"alias": alias,
		})

		bob.JoinRoom(t, aliasRoomID, []string{})

		res := alice.SlidingSyncUntilMembership(t, "", aliasRoomID, bob, "join")
		if len(res.Rooms[aliasRoomID].Heroes) > 0 {
			t.Errorf("expected no heroes, got %#v", res.Rooms[aliasRoomID].Heroes)
		}
	})

	t.Run("can set heroes=true on room subscriptions", func(t *testing.T) {
		subRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
		bob.JoinRoom(t, subRoomID, []string{})

		res := alice.SlidingSyncUntilMembership(t, "", subRoomID, bob, "join")
		if c := len(res.Rooms[subRoomID].Heroes); c > 1 {
			t.Errorf("expected 1 room hero, got %d", c)
		}
		if gotUserID := res.Rooms[subRoomID].Heroes[0].ID; gotUserID != bob.UserID {
			t.Errorf("expected userID %q, got %q", gotUserID, bob.UserID)
		}
	})

	t.Run("can set heroes=true in lists", func(t *testing.T) {
		listRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
		bob.JoinRoom(t, listRoomID, []string{})

		res := alice.SlidingSyncUntil(t, "", sync3.Request{
			Lists: map[string]sync3.RequestList{
				"all_rooms": {
					Ranges: sync3.SliceRanges{{0, 20}},
					RoomSubscription: sync3.RoomSubscription{
						Heroes:        &boolTrue,
						TimelineLimit: 10,
					},
				},
			},
		}, func(response *sync3.Response) error {
			r, ok := response.Rooms[listRoomID]
			if !ok {
				return fmt.Errorf("room %q not in response", listRoomID)
			}
			// wait for bob to be joined
			for _, ev := range r.Timeline {
				if gjson.GetBytes(ev, "type").Str != "m.room.member" {
					continue
				}
				if gjson.GetBytes(ev, "state_key").Str != bob.UserID {
					continue
				}
				if gjson.GetBytes(ev, "content.membership").Str == "join" {
					return nil
				}
			}
			return fmt.Errorf("%s is not joined to room %q", bob.UserID, listRoomID)
		})
		if c := len(res.Rooms[listRoomID].Heroes); c > 1 {
			t.Errorf("expected 1 room hero, got %d", c)
		}
		if gotUserID := res.Rooms[listRoomID].Heroes[0].ID; gotUserID != bob.UserID {
			t.Errorf("expected userID %q, got %q", gotUserID, bob.UserID)
		}
	})
}

// test invite/join counts update and are accurate
func TestMemberCounts(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	charlie := registerNewUser(t)

	firstRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "First"})
	alice.InviteRoom(t, firstRoomID, bob.UserID)
	secondRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "Second"})
	alice.InviteRoom(t, secondRoomID, bob.UserID)
	charlie.JoinRoom(t, secondRoomID, nil)
	t.Logf("first %s second %s", firstRoomID, secondRoomID)

	// sync as bob, we should see 2 invited rooms with the same join counts so as not to leak join counts
	res := bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(2), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 1, []string{firstRoomID, secondRoomID}, true),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		firstRoomID: {
			m.MatchRoomInitial(true),
			m.MatchInviteCount(1),
			m.MatchJoinCount(1),
			MatchRoomInviteState([]Event{
				{
					Type:     "m.room.name",
					StateKey: ptr(""),
					Content: map[string]interface{}{
						"name": "First",
					},
				},
			}, true),
		},
		secondRoomID: {
			m.MatchRoomInitial(true),
			m.MatchInviteCount(1),
			m.MatchJoinCount(1),
			MatchRoomInviteState([]Event{
				{
					Type:     "m.room.name",
					StateKey: ptr(""),
					Content: map[string]interface{}{
						"name": "Second",
					},
				},
			}, true),
		},
	}))

	// join both rooms, the counts should now reflect reality
	bob.JoinRoom(t, firstRoomID, nil)
	bob.JoinRoom(t, secondRoomID, nil)
	alice.SlidingSyncUntilMembership(t, "", firstRoomID, bob, "join")
	alice.SlidingSyncUntilMembership(t, "", secondRoomID, bob, "join")

	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		firstRoomID: {
			m.MatchRoomInitial(true),
			m.MatchInviteCount(0),
			m.MatchJoinCount(2),
		},
		secondRoomID: {
			m.MatchRoomInitial(true),
			m.MatchInviteCount(0),
			m.MatchJoinCount(3),
		},
	}))

	// sending a message shouldn't update the count as it wastes bandwidth
	charlie.SendEventSynced(t, secondRoomID, Event{
		Type:    "m.room.message",
		Content: map[string]interface{}{"body": "ping", "msgtype": "m.text"},
	})
	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		secondRoomID: {
			m.MatchRoomInitial(false),
			m.MatchNoInviteCount(),
			m.MatchJoinCount(0), // omitempty
		},
	}))

	// leaving a room should update the count
	charlie.LeaveRoom(t, secondRoomID)
	bob.MustSyncUntil(t, SyncReq{}, SyncLeftFrom(charlie.UserID, secondRoomID))

	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		secondRoomID: {
			m.MatchRoomInitial(false),
			m.MatchNoInviteCount(),
			m.MatchJoinCount(2),
		},
	}))
}
