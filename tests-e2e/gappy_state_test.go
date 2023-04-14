package syncv3_test

import (
	"fmt"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/gjson"
	"testing"
)

func TestGappyState(t *testing.T) {
	t.Log("Alice registers on the homeserver.")
	alice := registerNewUser(t)

	t.Log("Alice creates a room")
	roomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})

	t.Log("Alice sends a message into that room")
	alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello, world!",
		},
	})

	t.Log("Alice logs out of her first device.")
	alice.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "logout"})

	t.Log("Alice logs in again on her second device.")
	alice.Login(t, "password", "device2")

	t.Log("Alice changes the room name while the proxy isn't polling.")
	nameContent := map[string]interface{}{"name": "potato"}
	alice.SetState(t, roomID, "m.room.name", "", nameContent)

	t.Log("Alice sends lots of message events (more than the poller will request in a timeline.")
	var latestMessageID string
	for i := 0; i < 51; i++ {
		latestMessageID = alice.SendEventUnsynced(t, roomID, Event{
			Type: "m.room.message",
			Content: map[string]interface{}{
				"msgtype": "m.text",
				"body":    fmt.Sprintf("Message number %d", i),
			},
		})
	}

	t.Log("Alice requests an initial sliding sync on device 2.")
	syncResp := alice.SlidingSync(t,
		sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Ranges: [][2]int64{{0, 20}},
				},
			},
		},
	)

	t.Log("She should her latest message with the room updated")
	// TODO: Behind the scenes, this should have created a new poller which did a v2
	// initial sync. The v2 response should include the full state of the room, which
	// we should relay in the sync response.
	m.MatchResponse(
		t,
		syncResp,
		m.LogResponse(t),
		m.MatchRoomSubscription(
			roomID,
			m.MatchRoomName("potato"),
			func(r sync3.Room) error {
				lastReceivedEventID := gjson.ParseBytes(r.Timeline[len(r.Timeline)-1]).Get("event_id").Str
				if lastReceivedEventID != latestMessageID {
					return fmt.Errorf("last message in response is %s, expected %s", lastReceivedEventID, latestMessageID)
				}
				return nil
			},
		),
	)
}
