package syncv3_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/complement/client"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/gjson"
)

func TestRedactionsAreRedactedWherePossible(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	room := alice.MustCreateRoom(t, map[string]any{"preset": "public_chat"})

	eventID := alice.SendEventSynced(t, room, b.Event{
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
	redactionEventID := alice.MustSendRedaction(t, room, map[string]interface{}{}, eventID)

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

func TestRedactingRoomStateIsReflectedInNextSync(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	bob := registerNamedUser(t, "bob")

	t.Log("Alice creates a room, then sets a room alias and name.")
	room := alice.MustCreateRoom(t, map[string]any{
		"preset": "public_chat",
	})

	alias := fmt.Sprintf("#%s-%d:%s", t.Name(), time.Now().Unix(), alice.Domain)
	alice.MustDo(t, "PUT", []string{"_matrix", "client", "v3", "directory", "room", alias},
		client.WithJSONBody(t, map[string]any{"room_id": room}),
	)
	aliasID := alice.Unsafe_SendEventUnsynced(t, room, b.Event{
		Type:     "m.room.canonical_alias",
		StateKey: ptr(""),
		Content: map[string]interface{}{
			"alias": alias,
		},
	})

	const naughty = "naughty room for naughty people"
	nameID := alice.Unsafe_SendEventUnsynced(t, room, b.Event{
		Type:     "m.room.name",
		StateKey: ptr(""),
		Content: map[string]interface{}{
			"name": naughty,
		},
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
	redactionID := alice.MustSendRedaction(t, room, map[string]interface{}{}, nameID)

	t.Log("Alice syncs until she sees her redaction.")
	res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, m.MatchRoomSubscription(
		room,
		MatchRoomTimelineMostRecent(1, []Event{{ID: redactionID}}),
	))

	t.Log("The room name should have been redacted, falling back to the canonical alias.")
	m.MatchResponse(t, res, m.MatchRoomSubscription(room, m.MatchRoomName(alias)))

	t.Log("Alice sets a room avatar.")
	avatarURL := alice.UploadContent(t, smallPNG, "avatar.png", "image/png")
	avatarID := alice.Unsafe_SendEventUnsynced(t, room, b.Event{
		Type:     "m.room.avatar",
		StateKey: ptr(""),
		Content: map[string]interface{}{
			"url": avatarURL,
		},
	})

	t.Log("Alice waits to see the avatar.")
	res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, m.MatchRoomSubscription(room, m.MatchRoomAvatar(avatarURL)))

	t.Log("Alice redacts the avatar.")
	redactionID = alice.MustSendRedaction(t, room, map[string]interface{}{}, avatarID)

	t.Log("Alice sees the avatar revert to blank.")
	res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, m.MatchRoomSubscription(room, m.MatchRoomUnsetAvatar()))

	t.Log("Bob joins the room, with a custom displayname.")
	const bobDisplayName = "bob mortimer"
	bob.SetDisplayname(t, bobDisplayName)
	bob.JoinRoom(t, room, nil)

	t.Log("Alice sees Bob join.")
	res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, m.MatchRoomSubscription(room,
		MatchRoomTimelineMostRecent(1, []Event{{
			StateKey: ptr(bob.UserID),
			Type:     "m.room.member",
			Content: map[string]any{
				"membership":  "join",
				"displayname": bobDisplayName,
			},
		}}),
	))
	// Extract Bob's join ID because https://github.com/matrix-org/matrix-spec-proposals/pull/2943 doens't exist grrr
	timeline := res.Rooms[room].Timeline
	bobJoinID := gjson.GetBytes(timeline[len(timeline)-1], "event_id").Str

	t.Log("Alice redacts the alias.")
	redactionID = alice.MustSendRedaction(t, room, map[string]interface{}{}, aliasID)

	t.Log("Alice sees the room name reset to Bob's display name.")
	res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, m.MatchRoomSubscription(room, m.MatchRoomName(bobDisplayName)))

	t.Log("Bob redacts his membership")
	redactionID = bob.MustSendRedaction(t, room, map[string]interface{}{}, bobJoinID)

	t.Log("Alice sees the room name reset to Bob's username.")
	res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, m.MatchRoomSubscription(room, m.MatchRoomName(bob.UserID)))
}
