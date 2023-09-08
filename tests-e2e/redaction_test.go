package syncv3_test

import (
	"testing"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/gjson"
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
	// introspect the unsigned key a bit more, we don't know all the fields so can't use a matcher
	gotEvent := gjson.ParseBytes(res.Rooms[room].Timeline[len(res.Rooms[room].Timeline)-2])
	redactedBecause := gotEvent.Get("unsigned.redacted_because")
	if !redactedBecause.Exists() {
		t.Fatalf("unsigned.redacted_because must exist, but it doesn't. Got: %v", gotEvent.Raw)
	}
	// assert basic fields
	assertEqual(t, "event_id mismatch", redactedBecause.Get("event_id").Str, redactionEventID)
	assertEqual(t, "sender mismatch", redactedBecause.Get("sender").Str, alice.UserID)
	assertEqual(t, "type mismatch", redactedBecause.Get("type").Str, "m.room.redaction")
	if !redactedBecause.Get("content").Exists() {
		t.Fatalf("unsigned.redacted_because.content must exist, but it doesn't. Got: %v", gotEvent.Raw)
	}

}
