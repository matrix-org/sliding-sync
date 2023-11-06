package syncv3_test

import (
	"testing"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// Regression test to make sure that given:
// - Alice invite Bob
// - Alice send message
// - Bob join room
// The proxy returns either:
// - all 3 events (if allowed by history visibility) OR
// - Bob's join only, with a suitable prev_batch token
// and never:
// - invite then join, omitting the msg.
func TestTimelineIsCorrectWhenTransitioningFromInviteToJoin(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	roomID := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "trusted_private_chat",
		"invite": []string{bob.UserID},
	})
	aliceRes := alice.SlidingSyncUntilMembership(t, "", roomID, bob, "invite")
	bobRes := bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 4,
				},
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	})
	m.MatchResponse(t, bobRes, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchInviteCount(1),
			m.MatchRoomHasInviteState(),
			MatchRoomTimeline([]Event{{
				Type:     "m.room.member",
				StateKey: &bob.UserID,
				Content: map[string]interface{}{
					"membership":  "invite",
					"displayname": bob.Localpart,
				},
			}}),
		},
	}))

	eventID := alice.Unsafe_SendEventUnsynced(t, roomID, b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "After invite, before join",
		},
	})
	aliceRes = alice.SlidingSyncUntilEventID(t, aliceRes.Pos, roomID, eventID)

	bob.MustJoinRoom(t, roomID, []string{"hs1"})
	aliceRes = alice.SlidingSyncUntilMembership(t, aliceRes.Pos, roomID, bob, "join")

	bobRes = bob.SlidingSync(t, sync3.Request{}, WithPos(bobRes.Pos))
	m.MatchResponse(t, bobRes, m.LogResponse(t))
}
