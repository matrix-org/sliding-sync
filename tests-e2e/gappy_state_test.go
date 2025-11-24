package syncv3_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// Test that state changes "missed" by a poller are injected back into the room when a
// future poller recieves a v2 incremental sync with a state block.
func TestGappyState(t *testing.T) {
	t.Log("Alice registers on the homeserver.")
	alice := registerNewUser(t)

	t.Log("Alice creates a room")
	firstRoomName := "Romeo Oscar Oscar Mike"
	roomID := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": firstRoomName})

	t.Log("Alice sends a message into that room")
	firstMessageID := alice.SendEventSynced(t, roomID, b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello, world!",
		},
	})

	t.Log("Alice requests an initial sliding sync on device 1.")
	syncResp := alice.SlidingSync(t,
		sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Ranges: [][2]int64{{0, 20}},
					RoomSubscription: sync3.RoomSubscription{
						TimelineLimit: 10,
					},
				},
			},
		},
	)
	m.MatchResponse(
		t,
		syncResp,
		m.MatchRoomSubscription(
			roomID,
			m.MatchRoomName(firstRoomName),
			MatchRoomTimelineMostRecent(1, []Event{{ID: firstMessageID}}),
		),
	)

	t.Log("Alice logs out of her first device.")
	alice.MustDo(t, "POST", []string{"_matrix", "client", "v3", "logout"})

	t.Log("Alice logs in again on her second device.")
	alice.Login(t, "password", "device2")

	t.Log("Alice changes the room name while the proxy isn't polling.")
	nameContent := map[string]interface{}{"name": "potato"}
	alice.Unsafe_SendEventUnsynced(t, roomID, b.Event{
		Type:     "m.room.name",
		StateKey: ptr(""),
		Content:  nameContent,
	})

	t.Log("Alice sends lots of other state events.")
	const numOtherState = 40
	for i := 0; i < numOtherState; i++ {
		alice.Unsafe_SendEventUnsynced(t, roomID, b.Event{
			Type:     "com.example.dummy",
			StateKey: ptr(fmt.Sprintf("%d", i)),
			Content:  map[string]any{},
		})
	}

	t.Log("Alice sends a batch of message events.")
	const numMessages = 20
	var lastMsgID string
	for i := 0; i < numMessages; i++ {
		lastMsgID = alice.Unsafe_SendEventUnsynced(t, roomID, b.Event{
			Type: "m.room.message",
			Content: map[string]interface{}{
				"msgtype": "m.text",
				"body":    fmt.Sprintf("Message number %d", i),
			},
		})
	}

	t.Logf("The proxy is now %d events behind the HS, which should trigger a limited sync", 1+numOtherState+numMessages)

	t.Log("Alice requests an initial sliding sync on device 2, with timeline limit big enough to see her first message at the start of the test.")
	syncResp = alice.SlidingSync(t,
		sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Ranges: [][2]int64{{0, 20}},
					RoomSubscription: sync3.RoomSubscription{
						TimelineLimit: 100,
					},
				},
			},
		},
	)

	// We're testing here that the state events from the gappy poll are NOT injected
	// into the timeline. The poll is only going to use timeline limit 1 because it's
	// the first poll on a new device. See integration test for a "proper" gappy poll.
	t.Log("She should see the updated room name, her most recent message, but NOT the state events in the gap nor messages from before the gap.")
	m.MatchResponse(
		t,
		syncResp,
		m.MatchRoomSubscription(
			roomID,
			m.MatchRoomName("potato"),
			MatchRoomTimelineMostRecent(1, []Event{{ID: lastMsgID}}),
			func(r sync3.Room) error {
				for _, rawEv := range r.Timeline {
					var ev Event
					err := json.Unmarshal(rawEv, &ev)
					if err != nil {
						t.Fatal(err)
					}
					// Shouldn't see the state events, only messages
					if ev.Type != "m.room.message" {
						return fmt.Errorf("timeline contained event %s of type %s (expected m.room.message)", ev.ID, ev.Type)
					}
					if ev.ID == firstMessageID {
						return fmt.Errorf("timeline contained first message from before the gap")
					}
				}
				return nil
			},
		),
	)
}
