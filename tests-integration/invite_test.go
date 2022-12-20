package syncv3

import (
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

func TestStuckInvites(t *testing.T) {
	boolTrue := true
	// setup code
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	preSyncInviteRoomID := "!pre:localhost"
	postSyncInviteRoomID := "!post:localhost"

	inviteState := createRoomState(t, bob, time.Now())
	inviteState = append(inviteState, testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{
		"membership": "invite",
	}))

	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Invite: map[string]sync2.SyncV2InviteResponse{
				preSyncInviteRoomID: {
					InviteState: sync2.EventsResponse{
						Events: inviteState,
					},
				},
			},
		},
	})

	// initial sync, should see the invite
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 10}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 10, []string{preSyncInviteRoomID}),
	)))

	// live stream invite
	inviteState2 := createRoomState(t, bob, time.Now())
	inviteState2 = append(inviteState2, testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{
		"membership": "invite",
	}))
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Invite: map[string]sync2.SyncV2InviteResponse{
				postSyncInviteRoomID: {
					InviteState: sync2.EventsResponse{
						Events: inviteState2,
					},
				},
			},
		},
	})
	v2.waitUntilEmpty(t, alice)

	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 10}},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(2), m.MatchV3Ops(
		m.MatchV3DeleteOp(1),
		m.MatchV3InsertOp(0, postSyncInviteRoomID),
	)))

	// accept both invites: replace the invite (last state event)
	inviteState[len(inviteState)-1] = testutils.NewJoinEvent(t, alice, testutils.WithUnsigned(map[string]interface{}{
		"prev_content": map[string]string{
			"membership": "invite",
		},
	}))
	inviteState2[len(inviteState2)-1] = testutils.NewJoinEvent(t, alice, testutils.WithUnsigned(map[string]interface{}{
		"prev_content": map[string]string{
			"membership": "invite",
		},
	}))
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: preSyncInviteRoomID,
				events: inviteState,
			},
				roomEvents{
					roomID: postSyncInviteRoomID,
					events: inviteState2,
				}),
		},
	})
	v2.waitUntilEmpty(t, alice)

	// the entries are removed
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 10}},
			},
		},
	})
	// not asserting the ops here as they could be DELETE 1, DELETE 0 or DELETE 0, DELETE 0 which is hard
	// to assert.
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(0)))

	// restart the server
	v3.restart(t, v2, pqString)

	// now query for invites: there should be none if we are clearing the DB correctly.
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 10}},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscriptionsStrict(nil))
}
