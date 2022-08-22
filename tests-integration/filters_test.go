package syncv3

import (
	"encoding/json"
	"testing"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
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
			RoomType: roomType,
		},
		roomID: {},
		otherRoomID: {
			RoomType: otherRoomType,
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

func TestFiltersTags(t *testing.T) {
	tagFav := "m.favourite"
	tagLow := "m.lowpriority"
	rig := NewTestRig(t)
	defer rig.Finish()
	fav1RoomID := "!fav1:localhost"
	fav2RoomID := "!fav2:localhost"
	favAndLowRoomID := "!favlow:localhost"
	low1RoomID := "!low1:localhost"
	low2RoomID := "!low2:localhost"
	rig.SetupV2RoomsForUser(t, alice, NoFlush, map[string]RoomDescriptor{
		fav1RoomID: {
			Tags: map[string]float64{
				tagFav: 0.5,
			},
		},
		fav2RoomID: {
			Tags: map[string]float64{
				tagFav: 0.3,
			},
		},
		favAndLowRoomID: {
			Tags: map[string]float64{
				tagFav: 0.5,
				tagLow: 0.9,
			},
		},
		low1RoomID: {
			Tags: map[string]float64{
				tagLow: 0.2,
			},
		},
		low2RoomID: {
			Tags: map[string]float64{
				tagLow: 0.92,
			},
		},
	})
	aliceToken := rig.Token(alice)
	res := rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20},
				},
				Filters: &sync3.RequestFilters{
					Tags: []string{tagFav},
				},
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20},
				},
				Filters: &sync3.RequestFilters{
					Tags: []string{tagLow},
				},
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20},
				},
				Filters: &sync3.RequestFilters{
					Tags: []string{tagFav, tagLow},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(3), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 20, []string{fav1RoomID, fav2RoomID, favAndLowRoomID}, true),
	)), m.MatchList(1, m.MatchV3Count(3), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 20, []string{low1RoomID, low2RoomID, favAndLowRoomID}, true),
	)), m.MatchList(2, m.MatchV3Count(5), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 20, []string{fav1RoomID, fav2RoomID, favAndLowRoomID, low1RoomID, low2RoomID}, true),
	)))

	// first bump the fav1 room
	rig.FlushEvent(t, alice, fav1RoomID, testutils.NewMessageEvent(t, alice, "Hi"))
	res = rig.V3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{Ranges: sync3.SliceRanges{{0, 20}}},
			{Ranges: sync3.SliceRanges{{0, 20}}},
			{Ranges: sync3.SliceRanges{{0, 20}}},
		},
	})

	// now nuke it by removing the tag
	rig.V2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				fav1RoomID: {
					AccountData: sync2.EventsResponse{
						Events: []json.RawMessage{
							testutils.NewAccountData(t, "m.tag", map[string]interface{}{}), // empty content
						},
					},
				},
			},
		},
	})
	rig.V2.waitUntilEmpty(t, alice)

	// we should see DELETEs
	res = rig.V3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{Ranges: sync3.SliceRanges{{0, 20}}},
			{Ranges: sync3.SliceRanges{{0, 20}}},
			{Ranges: sync3.SliceRanges{{0, 20}}},
		},
	})
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(2), m.MatchV3Ops(
		m.MatchV3DeleteOp(0),
	)), m.MatchList(1, m.MatchV3Ops()), m.MatchList(2, m.MatchV3Count(4), m.MatchV3Ops(
		m.MatchV3DeleteOp(0),
	)))

	// check not_tags works
	res = rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20},
				},
				Filters: &sync3.RequestFilters{
					Tags: []string{tagFav},
				},
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20},
				},
				Filters: &sync3.RequestFilters{
					NotTags: []string{tagFav},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList(0, m.MatchV3Count(2), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 20, []string{fav2RoomID, favAndLowRoomID}, true),
	)), m.MatchList(1, m.MatchV3Count(3), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 20, []string{low1RoomID, low2RoomID, fav1RoomID}, true),
	)))
}
