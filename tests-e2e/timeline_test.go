package syncv3_test

import (
	"encoding/json"
	"testing"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/complement/must"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// Regression test to make sure that given:
// - Alice invite Bob (shared history visibility)
// - Alice send message
// - Bob join room
// The proxy returns:
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
	alice.SlidingSyncUntilMembership(t, aliceRes.Pos, roomID, bob, "join")

	bobRes = bob.SlidingSync(t, sync3.Request{}, WithPos(bobRes.Pos))
	m.MatchResponse(t, bobRes, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			// only the join event, this specifically does a length check
			MatchRoomTimeline([]Event{
				{
					Type:     "m.room.member",
					StateKey: &bob.UserID,
					Content: map[string]interface{}{
						"membership":  "join",
						"displayname": bob.Localpart,
					},
				},
			}),
		},
	}))
	// pull out the prev batch token and use it to make sure we see the correct timeline
	prevBatch := bobRes.Rooms[roomID].PrevBatch
	must.NotEqual(t, prevBatch, "", "missing prev_batch")

	scrollback := bob.Scrollback(t, roomID, prevBatch, 2)
	// we should only see the message and our invite, in that order
	chunk := scrollback.Get("chunk").Array()
	var sbEvents []json.RawMessage
	for _, e := range chunk {
		sbEvents = append(sbEvents, json.RawMessage(e.Raw))
	}
	must.Equal(t, len(chunk), 2, "chunk length mismatch")
	must.NotError(t, "chunk mismatch", eventsEqual([]Event{
		{
			Type:   "m.room.message",
			Sender: alice.UserID,
			Content: map[string]interface{}{
				"msgtype": "m.text",
				"body":    "After invite, before join",
			},
		},
		{
			Type:     "m.room.member",
			Sender:   alice.UserID,
			StateKey: &bob.UserID,
			Content: map[string]interface{}{
				"membership":  "invite",
				"displayname": bob.Localpart,
			},
		},
	}, sbEvents))
}
