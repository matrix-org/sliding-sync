package syncv3

import (
	"testing"

	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils/m"
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
	m.MatchResponse(t, res, m.MatchLists(
		[]m.ListMatcher{
			m.MatchV3Count(1),
			m.MatchV3Ops(
				m.MatchV3SyncOp(0, 1, []string{encryptedRoomID}),
			),
		},
		[]m.ListMatcher{
			m.MatchV3Count(1),
			m.MatchV3Ops(
				m.MatchV3SyncOp(0, 1, []string{unencryptedRoomID}),
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
	m.MatchResponse(t, res, m.MatchLists(
		[]m.ListMatcher{
			m.MatchV3Count(2),
			m.MatchV3Ops(
				m.MatchV3DeleteOp(1), m.MatchV3InsertOp(0, unencryptedRoomID),
			),
		},
		[]m.ListMatcher{
			m.MatchV3Count(0),
			m.MatchV3Ops(
				m.MatchV3DeleteOp(0),
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
	m.MatchResponse(t, res, m.MatchLists(
		[]m.ListMatcher{
			m.MatchV3Count(2),
			m.MatchV3Ops(
				m.MatchV3SyncOp(0, 1, []string{unencryptedRoomID, encryptedRoomID}),
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
	m.MatchResponse(t, res, m.MatchLists(
		[]m.ListMatcher{
			m.MatchV3Count(0),
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
	m.MatchResponse(t, res, m.MatchLists(
		[]m.ListMatcher{
			m.MatchV3Count(1),
			m.MatchV3Ops(
				m.MatchV3SyncOp(0, 20, []string{roomID}),
			),
		},
		[]m.ListMatcher{
			m.MatchV3Count(0),
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
	m.MatchResponse(t, res, m.MatchLists(
		[]m.ListMatcher{
			m.MatchV3Count(0),
			m.MatchV3Ops(
				m.MatchV3DeleteOp(0),
			),
		},
		[]m.ListMatcher{
			m.MatchV3Count(1),
			m.MatchV3Ops(
				m.MatchV3InsertOp(0, roomID),
			),
		},
	))
}

func TestFiltersRoomName(t *testing.T) {
	rig := NewTestRig(t)
	defer rig.Finish()
	ridApple := "!a:localhost"
	ridPear := "!b:localhost"
	ridLemon := "!c:localhost"
	ridOrange := "!d:localhost"
	ridPineapple := "!e:localhost"
	ridBanana := "!f:localhost"
	ridKiwi := "!g:localhost"
	rig.SetupV2RoomsForUser(t, alice, NoFlush, map[string]RoomDescriptor{
		ridApple: {
			Name: "apple",
		},
		ridPear: {
			Name: "PEAR",
		},
		ridLemon: {
			Name: "Lemon Lemon",
		},
		ridOrange: {
			Name: "ooooorange",
		},
		ridPineapple: {
			Name: "PineApple",
		},
		ridBanana: {
			Name: "BaNaNaNaNaNaNaN",
		},
		ridKiwi: {
			Name: "kiwiwiwiwiwiwi",
		},
	})
	aliceToken := rig.Token(alice)

	// make sure the room name filter works
	res := rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					RoomNameFilter: "a",
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchLists(
		[]m.ListMatcher{
			m.MatchV3Count(5),
			m.MatchV3Ops(
				m.MatchV3SyncOp(0, 20, []string{
					ridApple, ridPear, ridOrange, ridPineapple, ridBanana,
				}, true),
			),
		},
	))

	// refine the filter
	res = rig.V3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					RoomNameFilter: "app",
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchLists(
		[]m.ListMatcher{
			m.MatchV3Count(2),
			m.MatchV3Ops(
				m.MatchV3InvalidateOp(0, 20),
				m.MatchV3SyncOp(0, 20, []string{
					ridApple, ridPineapple,
				}, true),
			),
		},
	))
}

func TestFiltersRoomTypes(t *testing.T) {
	rig := NewTestRig(t)
	defer rig.Finish()
	spaceRoomID := "!spaceRoomID:localhost"
	otherRoomID := "!other:localhost"
	roomID := "!roomID:localhost"
	roomType := "m.space"
	otherRoomType := "something_else"
	invalid := "lalalalaala"
	rig.SetupV2RoomsForUser(t, alice, NoFlush, map[string]RoomDescriptor{
		spaceRoomID: {
			MembershipOfSyncer: "join",
			RoomType:           roomType,
		},
		roomID: {
			MembershipOfSyncer: "join",
		},
		otherRoomID: {
			MembershipOfSyncer: "join",
			RoomType:           otherRoomType,
		},
	})
	aliceToken := rig.Token(alice)

	// make sure the room_types and not_room_types filters works
	res := rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			// returns spaceRoomID only due to direct match
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					RoomTypes: []*string{&roomType},
				},
			},
			// returns roomID only due to direct match (null = things without a room type)
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					RoomTypes: []*string{nil},
				},
			},
			// returns roomID and otherRoomID due to exclusion
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					NotRoomTypes: []*string{&roomType},
				},
			},
			// returns otherRoomID due to otherRoomType inclusive, roomType is excluded (override)
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					RoomTypes:    []*string{&roomType, &otherRoomType},
					NotRoomTypes: []*string{&roomType},
				},
			},
			// returns no rooms as filtered room type isn't set on any rooms
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					RoomTypes: []*string{&invalid},
				},
			},
			// returns all rooms as filtered not room type isn't set on any rooms
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					NotRoomTypes: []*string{&invalid},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchLists(
		[]m.ListMatcher{
			m.MatchV3Count(1), m.MatchV3Ops(m.MatchV3SyncOp(0, 20, []string{spaceRoomID})),
		},
		[]m.ListMatcher{
			m.MatchV3Count(1), m.MatchV3Ops(m.MatchV3SyncOp(0, 20, []string{roomID})),
		},
		[]m.ListMatcher{
			m.MatchV3Count(2), m.MatchV3Ops(m.MatchV3SyncOp(0, 20, []string{roomID, otherRoomID}, true)),
		},
		[]m.ListMatcher{
			m.MatchV3Count(1), m.MatchV3Ops(m.MatchV3SyncOp(0, 20, []string{otherRoomID})),
		},
		[]m.ListMatcher{
			m.MatchV3Count(0),
		},
		[]m.ListMatcher{
			m.MatchV3Count(3), m.MatchV3Ops(m.MatchV3SyncOp(0, 20, []string{roomID, otherRoomID, spaceRoomID}, true)),
		},
	))
}
