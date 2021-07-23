package syncv3

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
)

// This tests all the moving parts for the sync v3 server. It does the following:
// - Initialise a single room between alice on bob on the v2 side. Tests that the v2 poller is glued in to the storage code.
// - Specify a typing filter and call v3 sync. This should block and timeout as there are no typing notifs for this room.
//   Tests that the notifier can block and time out.
// - Set bob to typing in the room and call v3 sync WITHOUT a typing filter. Tests that the server remembers filters
//   and that the typing filter works.
// - Call v3 sync again and after 100ms inject a new typing response into the v2 stream. Tests that v2 responses poke the
//   notifier which then pokes the v3 sync.
func TestHandler(t *testing.T) {
	alice := "@alice:localhost"
	aliceBearer := "Bearer alice_access_token"
	bob := "@bob:localhost"
	charlie := "@charlie:localhost"
	roomID := "!foo:localhost"

	server, v2Client := newSync3Server(t)
	aliceV2Stream := v2Client.v2StreamForUser(alice, aliceBearer)

	// prepare a response from v2
	v2Resp := &sync2.SyncResponse{
		NextBatch: "don't care",
	}
	v2Resp.Rooms.Join = make(map[string]sync2.SyncV2JoinResponse)
	v2Resp.Rooms.Join[roomID] = sync2.SyncV2JoinResponse{
		State: struct {
			Events []json.RawMessage `json:"events"`
		}{
			Events: []json.RawMessage{
				marshalJSON(t, map[string]interface{}{
					"event_id": "$1", "sender": bob, "type": "m.room.create", "state_key": "", "content": map[string]interface{}{
						"creator": bob,
					}}),
				marshalJSON(t, map[string]interface{}{
					"event_id": "$2", "sender": bob, "type": "m.room.join_rules", "state_key": "", "content": map[string]interface{}{
						"join_rule": "public",
					}}),
				marshalJSON(t, map[string]interface{}{
					"event_id": "$3", "sender": bob, "type": "m.room.member", "state_key": bob, "content": map[string]interface{}{
						"membership": "join",
					}}),
				marshalJSON(t, map[string]interface{}{
					"event_id": "$4", "sender": alice, "type": "m.room.member", "state_key": alice, "content": map[string]interface{}{
						"membership": "join",
					}}),
			},
		},
	}
	aliceV2Stream <- v2Resp

	// fresh user should make a new session and start polling, getting these events above.
	// however, we didn't ask for them so they shouldn't be returned and should timeout with no data
	v3resp := mustDoSync3Request(t, server, aliceBearer, "", map[string]interface{}{
		"typing": map[string]interface{}{
			"room_id": roomID,
		},
	})
	if v3resp.Typing != nil {
		t.Fatalf("expected no data due to timeout, got data: %+v", v3resp.Typing)
	}

	// now set bob to typing
	v2Resp = &sync2.SyncResponse{
		NextBatch: "still don't care",
	}
	v2Resp.Rooms.Join = make(map[string]sync2.SyncV2JoinResponse)
	v2Resp.Rooms.Join[roomID] = sync2.SyncV2JoinResponse{
		Ephemeral: struct {
			Events []json.RawMessage `json:"events"`
		}{
			Events: []json.RawMessage{
				marshalJSON(t, map[string]interface{}{
					"type": "m.typing", "room_id": roomID, "content": map[string]interface{}{
						"user_ids": []string{bob},
					},
				}),
			},
		},
	}
	aliceV2Stream <- v2Resp

	// 2nd request with no special args should remember we want the typing notif
	v3resp = mustDoSync3Request(t, server, aliceBearer, v3resp.Next, map[string]interface{}{})

	// Check that the response returns bob typing
	if v3resp.Typing == nil {
		t.Fatalf("no typing response, wanted one")
	}
	if len(v3resp.Typing.UserIDs) != 1 {
		t.Fatalf("typing got %d users, want 1: %v", len(v3resp.Typing.UserIDs), v3resp.Typing.UserIDs)
	}
	if v3resp.Typing.UserIDs[0] != bob {
		t.Fatalf("typing got %s want %s", v3resp.Typing.UserIDs[0], bob)
	}

	// inject a new v2 response after 200ms which should wake up the sync stream
	go func() {
		time.Sleep(200 * time.Millisecond)
		// now set charlie to typing
		v2Resp = &sync2.SyncResponse{
			NextBatch: "still still don't care",
		}
		v2Resp.Rooms.Join = make(map[string]sync2.SyncV2JoinResponse)
		v2Resp.Rooms.Join[roomID] = sync2.SyncV2JoinResponse{
			Ephemeral: struct {
				Events []json.RawMessage `json:"events"`
			}{
				Events: []json.RawMessage{
					marshalJSON(t, map[string]interface{}{
						"type": "m.typing", "room_id": roomID, "content": map[string]interface{}{
							"user_ids": []string{charlie},
						},
					}),
				},
			},
		}
		aliceV2Stream <- v2Resp
	}()
	v3resp = mustDoSync3Request(t, server, aliceBearer, v3resp.Next, map[string]interface{}{})

	// Check that the response returns charlie typing
	if v3resp.Typing == nil {
		t.Fatalf("no typing response, wanted one")
	}
	if len(v3resp.Typing.UserIDs) != 1 {
		t.Fatalf("typing got %d users, want 1: %v", len(v3resp.Typing.UserIDs), v3resp.Typing.UserIDs)
	}
	if v3resp.Typing.UserIDs[0] != charlie {
		t.Fatalf("typing got %s want %s", v3resp.Typing.UserIDs[0], charlie)
	}
}
