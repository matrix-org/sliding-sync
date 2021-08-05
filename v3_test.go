package syncv3

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3/streams"
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

// Test to_device stream:
// - Injecting a to_device event gets received.
// - `limit` is honoured. Including server-side negotiation.
// - Repeating the request without having ACKed the position returns the event again.
// - After ACKing the position, going back to the old position returns no event.
// - If 2 sessions exist, both session must ACK the position before the event is deleted.
func TestHandlerToDevice(t *testing.T) {
	server, v2Client := newSync3Server(t)
	t.Run("Injecting a to_device event into the v2 stream gets received", func(t *testing.T) {
		alice := "@alice:localhost"
		aliceBearer := "Bearer alice_access_token"
		aliceV2Stream := v2Client.v2StreamForUser(alice, aliceBearer)

		// prepare a response from v2
		toDeviceEvent := gomatrixserverlib.SendToDeviceEvent{
			Sender:  alice,
			Type:    "to_device.test",
			Content: []byte(`{"foo":"bar"}`),
		}
		v2Resp := &sync2.SyncResponse{
			NextBatch: "don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{
					toDeviceEvent,
				},
			},
		}
		aliceV2Stream <- v2Resp

		v3resp := mustDoSync3Request(t, server, aliceBearer, "", map[string]interface{}{
			"to_device": map[string]interface{}{},
		})
		if v3resp.ToDevice == nil {
			t.Fatalf("expected to_device response, got none: %+v", v3resp)
		}
		if len(v3resp.ToDevice.Events) != 1 {
			t.Fatalf("expected 1 to_device message, got %d", len(v3resp.ToDevice.Events))
		}
		want, _ := json.Marshal(toDeviceEvent)
		if !bytes.Equal(v3resp.ToDevice.Events[0], want) {
			t.Fatalf("wrong event returned, got %s want %s", string(v3resp.ToDevice.Events[0]), string(want))
		}
	})
	t.Run("'limit' is honoured and fetches earliest events first'", func(t *testing.T) {
		numMsgs := 300
		userLimit := 20

		bob := "@bob:localhost"
		bobBearer := "Bearer bob_access_token"
		bobV2Stream := v2Client.v2StreamForUser(bob, bobBearer)

		// prepare a response from v2
		v2Resp := &sync2.SyncResponse{
			NextBatch: "don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{},
			},
		}

		for i := 0; i < numMsgs; i++ {
			v2Resp.ToDevice.Events = append(v2Resp.ToDevice.Events, gomatrixserverlib.SendToDeviceEvent{
				Sender:  bob,
				Type:    "to_device.test_limit",
				Content: []byte(fmt.Sprintf(`{"foo":"bar %d"}`, i)),
			})
		}

		bobV2Stream <- v2Resp

		v3resp := mustDoSync3Request(t, server, bobBearer, "", map[string]interface{}{
			"to_device": map[string]interface{}{
				"limit": userLimit,
			},
		})
		if v3resp.ToDevice == nil {
			t.Fatalf("expected to_device response, got none: %+v", v3resp)
		}
		if len(v3resp.ToDevice.Events) > userLimit {
			t.Fatalf("expected %d to_device message, got %d", userLimit, len(v3resp.ToDevice.Events))
		}
		// should return earliest events first
		for i := 0; i < userLimit; i++ {
			want, _ := json.Marshal(v2Resp.ToDevice.Events[i])
			if !bytes.Equal(v3resp.ToDevice.Events[i], want) {
				t.Fatalf("wrong event returned, got %s want %s", string(v3resp.ToDevice.Events[i]), string(want))
			}
		}
		// next request gets the next batch of limited events
		v3resp = mustDoSync3Request(t, server, bobBearer, v3resp.Next, map[string]interface{}{
			"to_device": map[string]interface{}{
				"limit": userLimit,
			},
		})
		if v3resp.ToDevice == nil {
			t.Fatalf("expected to_device response, got none: %+v", v3resp)
		}
		if len(v3resp.ToDevice.Events) > userLimit {
			t.Fatalf("expected %d to_device message, got %d", userLimit, len(v3resp.ToDevice.Events))
		}
		// should return earliest events first
		for i := 0; i < userLimit; i++ {
			j := userLimit + i
			want, _ := json.Marshal(v2Resp.ToDevice.Events[j])
			if !bytes.Equal(v3resp.ToDevice.Events[i], want) {
				t.Fatalf("wrong event returned, got %s want %s", string(v3resp.ToDevice.Events[i]), string(want))
			}
		}
	})
	t.Run("Repeating the request without having ACKed the position returns the event again", func(t *testing.T) {
		charlie := "@charlie:localhost"
		charlieBearer := "Bearer charlie_access_token"
		charlieV2Stream := v2Client.v2StreamForUser(charlie, charlieBearer)

		// prepare a response from v2
		v2Resp := &sync2.SyncResponse{
			NextBatch: "don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{
					{
						Sender:  charlie,
						Type:    "to_device.dummy-event-to-get-a-since-token",
						Content: []byte(`{"foo":"bar"}`),
					},
				},
			},
		}
		charlieV2Stream <- v2Resp
		v3respAnchor := mustDoSync3Request(t, server, charlieBearer, "", map[string]interface{}{
			"to_device": map[string]interface{}{},
		})
		// now we have a ?since= token we can use, inject the actual event
		toDeviceEvent := gomatrixserverlib.SendToDeviceEvent{
			Sender:  charlie,
			Type:    "to_device.test.this.should.work",
			Content: []byte(`{"foo":"bar2"}`),
		}
		v2Resp = &sync2.SyncResponse{
			NextBatch: "still don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{
					toDeviceEvent,
				},
			},
		}
		charlieV2Stream <- v2Resp

		// do the same request 3 times with the first since token. Because we never increment the
		// since token it should keep returning the same event
		var v3resp *streams.Response
		for i := 0; i < 3; i++ {
			v3resp = mustDoSync3Request(t, server, charlieBearer, v3respAnchor.Next, map[string]interface{}{
				"to_device": map[string]interface{}{},
			})
			if v3resp.ToDevice == nil {
				t.Fatalf("expected to_device response, got none: %+v", v3resp)
			}
			if len(v3resp.ToDevice.Events) != 1 {
				t.Fatalf("expected 1 to_device message, got %d", len(v3resp.ToDevice.Events))
			}
			want, _ := json.Marshal(toDeviceEvent)
			if !bytes.Equal(v3resp.ToDevice.Events[0], want) {
				t.Fatalf("wrong event returned, got %s want %s", string(v3resp.ToDevice.Events[0]), string(want))
			}
		}
		t.Run("After ACKing the position, going back to the old position returns no event", func(t *testing.T) {
			t.Logf("acking pos %v", v3resp.Next)
			// ACK the position
			_ = mustDoSync3Request(t, server, charlieBearer, v3resp.Next, map[string]interface{}{
				"to_device": map[string]interface{}{},
			})
			t.Logf("going back to pos %v", v3respAnchor.Next)
			// now requests for the previous position return nothing as the to_device event was deleted
			v3resp = mustDoSync3Request(t, server, charlieBearer, v3respAnchor.Next, map[string]interface{}{
				"to_device": map[string]interface{}{},
			})
			if v3resp.ToDevice == nil {
				t.Fatalf("expected to_device response, got none: %+v", v3resp)
			}
			if len(v3resp.ToDevice.Events) != 0 {
				t.Fatalf("expected 0 to_device message, got %d", len(v3resp.ToDevice.Events))
			}
		})
	})
	t.Run("If 2 sessions exist, both session must ACK the position before the event is deleted", func(t *testing.T) {
		doris := "@doris:localhost"
		dorisBearer := "Bearer doris_access_token"
		dorisV2Stream := v2Client.v2StreamForUser(doris, dorisBearer)
		// prepare a response from v2
		v2Resp := &sync2.SyncResponse{
			NextBatch: "don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{
					{
						Sender:  doris,
						Type:    "to_device.dummy-event-to-get-a-since-token",
						Content: []byte(`{"foo":"bar"}`),
					},
				},
			},
		}
		dorisV2Stream <- v2Resp
		// do 2 calls to /sync without a since token == make 2 sessions
		s1v3respAnchor := mustDoSync3Request(t, server, dorisBearer, "", map[string]interface{}{
			"to_device": map[string]interface{}{},
		})
		s2v3respAnchor := mustDoSync3Request(t, server, dorisBearer, "", map[string]interface{}{
			"to_device": map[string]interface{}{},
		})
		// now we have a ?since= token we can use, inject the actual event
		toDeviceEvent := gomatrixserverlib.SendToDeviceEvent{
			Sender:  doris,
			Type:    "to_device.test.this.should.work",
			Content: []byte(`{"foo":"bar2"}`),
		}
		v2Resp = &sync2.SyncResponse{
			NextBatch: "still don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{
					toDeviceEvent,
				},
			},
		}
		dorisV2Stream <- v2Resp
		// calling /sync with session 1 and session 2 now returns the event
		sinceValues := []string{s1v3respAnchor.Next, s2v3respAnchor.Next}
		for i, since := range sinceValues {
			v3resp := mustDoSync3Request(t, server, dorisBearer, since, map[string]interface{}{
				"to_device": map[string]interface{}{},
			})
			if v3resp.ToDevice == nil {
				t.Fatalf("expected to_device response, got none: %+v", v3resp)
			}
			if len(v3resp.ToDevice.Events) != 1 {
				t.Fatalf("expected 1 to_device message, got %d", len(v3resp.ToDevice.Events))
			}
			// update to the latest token
			sinceValues[i] = v3resp.Next
		}

		// up to this point neither session have ACKed the event even though they got it, because they
		// haven't made a request with the latest token, so do it with session 1.
		_ = mustDoSync3Request(t, server, dorisBearer, sinceValues[0], map[string]interface{}{
			"to_device": map[string]interface{}{},
		})

		// now, doing the old request on session 2 should still return the event as session 2 never
		// ACKed the event
		v3resp := mustDoSync3Request(t, server, dorisBearer, s2v3respAnchor.Next, map[string]interface{}{
			"to_device": map[string]interface{}{},
		})
		if v3resp.ToDevice == nil {
			t.Fatalf("expected to_device response, got none: %+v", v3resp)
		}
		if len(v3resp.ToDevice.Events) != 1 {
			t.Fatalf("expected 1 to_device message, got %d", len(v3resp.ToDevice.Events))
		}
	})
}
