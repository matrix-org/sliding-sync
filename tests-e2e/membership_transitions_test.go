package syncv3_test

import (
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils/m"
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
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
				Sort: []string{sync3.SortByRecency},
			},
		},
	})

	// bob should see the invited/joined rooms
	bobRes := bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
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
	m.MatchResponse(t, bobRes, m.MatchList(0, m.MatchV3Count(2), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 100, []string{inviteRoomID, joinRoomID}),
	)), m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
		inviteRoomID: {
			m.MatchRoomHighlightCount(1),
			m.MatchRoomInitial(true),
			m.MatchRoomRequiredState(nil),
			// TODO m.MatchRoomInviteState(inviteStrippedState.InviteState.Events),
		},
		joinRoomID: {},
	}),
	)

	// now bob accepts the invite
	bob.JoinRoom(t, inviteRoomID, nil)

	// the room should be updated with the initial flag set to replace what was in the invite state
	bobRes = bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					{0, 100},
				},
			},
		},
	}, WithPos(bobRes.Pos))
	m.MatchResponse(t, bobRes, m.MatchNoV3Ops(), m.MatchList(0, m.MatchV3Count(2)), m.MatchRoomSubscription(inviteRoomID,
		MatchRoomRequiredState([]Event{
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
		m.MatchRoomHighlightCount(0)))
}

func TestInviteRejection(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	// ensure that invite state correctly propagates. One room will already be in 'invite' state
	// prior to the first proxy sync, whereas the 2nd will transition.
	firstInviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": "First"})
	alice.InviteRoom(t, firstInviteRoomID, bob.UserID)
	secondInviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": "Second"})
	t.Logf("first %s second %s", firstInviteRoomID, secondInviteRoomID)

	// sync as bob, we should see 1 invite
	res := bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 20, []string{firstInviteRoomID}),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		firstInviteRoomID: {
			m.MatchRoomInitial(true),
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
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(2), m.MatchV3Ops(
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

	// the list should be purged
	res = bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(0), m.MatchV3Ops(
		m.MatchV3DeleteOp(1),
		m.MatchV3DeleteOp(0),
	)))

	// fresh sync -> no invites
	res = bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(nil), m.MatchList(0, m.MatchV3Count(0)))
}

func TestInviteAcceptance(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	// ensure that invite state correctly propagates. One room will already be in 'invite' state
	// prior to the first proxy sync, whereas the 2nd will transition.
	firstInviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": "First"})
	alice.InviteRoom(t, firstInviteRoomID, bob.UserID)
	secondInviteRoomID := alice.CreateRoom(t, map[string]interface{}{"preset": "private_chat", "name": "Second"})
	t.Logf("first %s second %s", firstInviteRoomID, secondInviteRoomID)

	// sync as bob, we should see 1 invite
	res := bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 20, []string{firstInviteRoomID}),
	)), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		firstInviteRoomID: {
			m.MatchRoomInitial(true),
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
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(2), m.MatchV3Ops(
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

	// now accept the invites
	bob.JoinRoom(t, firstInviteRoomID, nil)
	bob.JoinRoom(t, secondInviteRoomID, nil)
	bob.MustSyncUntil(t, SyncReq{Since: since}, SyncJoinedTo(bob.UserID, firstInviteRoomID), SyncJoinedTo(bob.UserID, secondInviteRoomID))
	// wait for the proxy to process the join response, no better way at present as we cannot introspect _when_ it's doing the next poll :/
	// could do if this were an integration test though.
	time.Sleep(100 * time.Millisecond)

	// the list should be purged
	res = bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(0), m.MatchV3Ops(
		m.MatchV3DeleteOp(1),
		m.MatchV3DeleteOp(0),
	)))

	// fresh sync -> no invites
	res = bob.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(nil), m.MatchList(0, m.MatchV3Count(0)))
}
