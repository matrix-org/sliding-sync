package syncv3_test

import (
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

	t.Log("Alice sends lots of message events (more than the poller will request in a timeline.")
	var latestMessageID string
	for i := 0; i < 51; i++ {
		latestMessageID = alice.Unsafe_SendEventUnsynced(t, roomID, b.Event{
			Type: "m.room.message",
			Content: map[string]interface{}{
				"msgtype": "m.text",
				"body":    fmt.Sprintf("Message number %d", i),
			},
		})
	}

	t.Log("Alice requests an initial sliding sync on device 2.")
	syncResp = alice.SlidingSync(t,
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

	t.Log("She should see her latest message with the room name updated")
	m.MatchResponse(
		t,
		syncResp,
		m.MatchRoomSubscription(
			roomID,
			m.MatchRoomName("potato"),
			MatchRoomTimelineMostRecent(1, []Event{{ID: latestMessageID}}),
		),
	)
}
