package syncv3

import (
	"testing"

	"github.com/matrix-org/sync-v3/sync3"
)

// Test that filters work initially and whilst streamed.
func TestFiltersEncryption(t *testing.T) {
	boolTrue := true
	boolFalse := false
	rig := NewTestRig(t)
	defer rig.Finish()
	encryptedRoomID := "!TestFilters_encrypted:localhost"
	unencryptedRoomID := "!TestFilters_unencrypted:localhost"
	rig.SetupV2RoomsForUser(t, alice, NoFlush, map[string]RoomDescriptor{
		encryptedRoomID: {
			IsEncrypted: true,
		},
		unencryptedRoomID: {
			IsEncrypted: false,
		},
	})
	aliceToken := rig.Token(alice)

	// connect and make sure either the encrypted room or not depending on what the filter says
	res := rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 1}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolTrue,
				},
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 1}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolFalse,
				},
			},
		},
	})
	MatchResponse(t, res, MatchLists(
		[]listMatcher{
			MatchV3Count(1),
			MatchV3Ops(
				MatchV3SyncOp(0, 1, []string{encryptedRoomID}),
			),
		},
		[]listMatcher{
			MatchV3Count(1),
			MatchV3Ops(
				MatchV3SyncOp(0, 1, []string{unencryptedRoomID}),
			),
		},
	))

	// change the unencrypted room into an encrypted room
	rig.EncryptRoom(t, alice, unencryptedRoomID)

	// now requesting the encrypted list should include it (added)
	res = rig.V3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 1}, // all rooms
				},
				// sticky; should remember filters
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 1}, // all rooms
				},
				// sticky; should remember filters
			},
		},
	})
	MatchResponse(t, res, MatchLists(
		[]listMatcher{
			MatchV3Count(2),
			MatchV3Ops(
				MatchV3DeleteOp(1), MatchV3InsertOp(0, unencryptedRoomID),
			),
		},
		[]listMatcher{
			MatchV3Count(0),
			MatchV3Ops(
				MatchV3DeleteOp(0),
			),
		},
	))

	// requesting the encrypted list from scratch returns 2 rooms now
	res = rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 1}, // all rooms
			},
			Filters: &sync3.RequestFilters{
				IsEncrypted: &boolTrue,
			},
		}},
	})
	MatchResponse(t, res, MatchLists(
		[]listMatcher{
			MatchV3Count(2),
			MatchV3Ops(
				MatchV3SyncOp(0, 1, []string{unencryptedRoomID, encryptedRoomID}),
			),
		},
	))

	// requesting the unencrypted stream from scratch returns 0 rooms
	res = rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 1}, // all rooms
			},
			Filters: &sync3.RequestFilters{
				IsEncrypted: &boolFalse,
			},
		}},
	})
	MatchResponse(t, res, MatchLists(
		[]listMatcher{
			MatchV3Count(0),
		},
	))
}

func TestFiltersInvite(t *testing.T) {
	boolTrue := true
	boolFalse := false
	rig := NewTestRig(t)
	defer rig.Finish()
	roomID := "!a:localhost"
	rig.SetupV2RoomsForUser(t, alice, NoFlush, map[string]RoomDescriptor{
		roomID: {
			MembershipOfSyncer: "invite",
		},
	})
	aliceToken := rig.Token(alice)

	// make sure the is_invite filter works
	res := rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolFalse,
				},
			},
		},
	})
	MatchResponse(t, res, MatchLists(
		[]listMatcher{
			MatchV3Count(1),
			MatchV3Ops(
				MatchV3SyncOp(0, 20, []string{roomID}),
			),
		},
		[]listMatcher{
			MatchV3Count(0),
		},
	))

	// Accept the invite
	rig.JoinRoom(t, alice, roomID)

	// now the room should move from one room to another
	res = rig.V3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				// sticky; should remember filters
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				// sticky; should remember filters
			},
		},
	})
	// the room swaps from the invite list to the join list
	MatchResponse(t, res, MatchLists(
		[]listMatcher{
			MatchV3Count(0),
			MatchV3Ops(
				MatchV3DeleteOp(0),
			),
		},
		[]listMatcher{
			MatchV3Count(1),
			MatchV3Ops(
				MatchV3InsertOp(0, roomID),
			),
		},
	))
}
