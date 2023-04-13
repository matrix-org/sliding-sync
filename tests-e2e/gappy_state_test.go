package syncv3_test

import (
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
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

	t.Log("Alice sets two pieces of room state while the proxy isn't polling.")
	nameContent := map[string]interface{}{"name": "potato"}
	nameID := alice.SetState(t, roomID, "m.room.name", "", nameContent)
	powerLevelState := map[string]interface{}{
		"users":          map[string]int{alice.UserID: 100},
		"events_default": 10,
	}
	powerLevelID := alice.SetState(t, roomID, "m.room.power_levels", "", powerLevelState)

	t.Log("Alice logs out of her second device.")
	alice.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "logout"})

	t.Log("Alice logs in again on her third device.")
	alice.Login(t, "password", "device3")

	t.Log("Alice does an initial sliding sync.")
	syncResp := alice.SlidingSync(t,
		sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Ranges: [][2]int64{{0, 20}},
				},
			},
		},
	)

	t.Log("She should see her latest state events in the response")
	// TODO: Behind the scenes, this should have created a new poller which did a v2
	// initial sync. The v2 response should include the full state of the room, which
	// we should relay in the sync response. Need to express this with a matcher.
	m.MatchResponse(t, syncResp, m.LogResponse(t))
}
