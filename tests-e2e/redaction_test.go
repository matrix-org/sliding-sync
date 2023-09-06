package syncv3_test

import (
	"testing"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

func TestRedactionsAreRedactedWherePossible(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	room := alice.CreateRoom(t, map[string]any{"preset": "public_chat"})

	eventID := alice.SendEventSynced(t, room, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "I will be redacted",
		},
	})

	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		room: {MatchRoomTimelineMostRecent(1, []Event{{ID: eventID, Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "I will be redacted",
		}}})},
	}))

	// redact the event
	redactionEventID := alice.RedactEvent(t, room, eventID)

	// see the redaction
	alice.SlidingSyncUntilEventID(t, res.Pos, room, redactionEventID)

	// now resync from scratch, the event should be redacted this time around.
	res = alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 2,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		room: {MatchRoomTimelineMostRecent(2, []Event{
			{ID: eventID, Content: map[string]interface{}{}},
			{ID: redactionEventID},
		})},
	}))

}
