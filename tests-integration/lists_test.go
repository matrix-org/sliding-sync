package syncv3

import (
	"testing"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

func TestListsAsKeys(t *testing.T) {
	boolTrue := true
	rig := NewTestRig(t)
	defer rig.Finish()
	encryptedRoomID := "!TestListsAsKeys_encrypted:localhost"
	unencryptedRoomID := "!TestListsAsKeys_unencrypted:localhost"
	rig.SetupV2RoomsForUser(t, alice, NoFlush, map[string]RoomDescriptor{
		encryptedRoomID: {
			IsEncrypted: true,
		},
		unencryptedRoomID: {
			IsEncrypted: false,
		},
	})
	aliceToken := rig.Token(alice)
	// make an encrypted room list, then bump both rooms and send a 2nd request with zero data
	// and make sure that we see the encrypted room message only (so the filter is still active)
	res := rig.V3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"enc": {
				Ranges: sync3.SliceRanges{{0, 20}},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		"enc": {
			m.MatchV3Count(1),
		},
	}), m.MatchRoomSubscription(encryptedRoomID))

	rig.FlushText(t, alice, encryptedRoomID, "bump encrypted")
	rig.FlushText(t, alice, unencryptedRoomID, "bump unencrypted")

	res = rig.V3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{})
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		"enc": {
			m.MatchV3Count(1),
		},
	}), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		encryptedRoomID: {},
	}))
}
