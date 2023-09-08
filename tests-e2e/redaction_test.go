package syncv3_test

import (
	"fmt"
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

func TestRedactingRoomNameIsReflectedInNextSync(t *testing.T) {
	alice := registerNamedUser(t, "alice")

	t.Log("Alice creates a room and sets a room name.")
	aliasLocalPart := t.Name()
	room := alice.CreateRoom(t, map[string]any{
		"room_alias_name": aliasLocalPart,
	})
	const naughty = "naughty room for naughty people"
	nameID := alice.SetState(t, room, "m.room.name", "", map[string]any{
		"name": naughty,
	})

	t.Log("Alice sliding syncs, subscribing to that room explicitly.")
	res := alice.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			room: {
				TimelineLimit: 20,
			},
		},
	})

	t.Log("Alice should see her room appear with its name.")
	m.MatchResponse(t, res, m.MatchRoomSubscription(room, m.MatchRoomName(naughty)))

	t.Log("Alice redacts the room name.")
	redactionID := alice.RedactEvent(t, room, nameID)

	t.Log("Alice syncs until she sees her redaction.")
	res = alice.SlidingSyncUntil(
		t,
		res.Pos,
		sync3.Request{},
		m.MatchRoomSubscription(
			room,
			MatchRoomTimelineMostRecent(1, []Event{
				{ID: redactionID},
			}),
		),
	)

	alias := fmt.Sprintf("#%s:synapse", aliasLocalPart)
	t.Log("The room name should have been redacted, falling back to the canonical alias.")
	m.MatchResponse(t, res, m.MatchRoomSubscription(room, m.MatchRoomName(alias)))
}
