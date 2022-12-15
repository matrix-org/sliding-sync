package syncv3_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/matrix-org/util"
	"github.com/tidwall/gjson"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
)

// Test that if you login to an account -> send a to-device message to this device -> initial proxy connection
// then you receive the to_device events.
func TestToDeviceDeliveryInitialLogin(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	bob.SendToDevice(t, "m.dummy", alice.UserID, alice.DeviceID, map[string]interface{}{})
	// loop until we see the event
	loopUntilToDeviceEvent(t, alice, nil, "", "m.dummy", bob.UserID)
}

// Test that if you are live streaming then you can get to_device events sent to you.
func TestToDeviceDeliveryStream(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	res := alice.SlidingSync(t, sync3.Request{
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Enabled: &boolTrue,
			},
		},
	})
	bob.SendToDevice(t, "m.dummy", alice.UserID, alice.DeviceID, map[string]interface{}{})

	// loop until we see the event
	loopUntilToDeviceEvent(t, alice, res, res.Extensions.ToDevice.NextBatch, "m.dummy", bob.UserID)
}

// Test that if were live streaming, then disconnect, have a to_device message sent to you, then reconnect,
// that you see the to_device message.
func TestToDeviceDeliveryReconnect(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	// start live stream, but ignore the pos to "disconnect"
	alice.SlidingSync(t, sync3.Request{
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Enabled: &boolTrue,
			},
		},
	})
	bob.SendToDevice(t, "m.dummy", alice.UserID, alice.DeviceID, map[string]interface{}{})
	// loop until we see the event
	loopUntilToDeviceEvent(t, alice, nil, "", "m.dummy", bob.UserID)
}

func TestToDeviceDropStaleKeyRequestsInitial(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	sendMessages := 3
	// send a few dummy messages, cancelling each other
	for i := 0; i < sendMessages; i++ {
		reqID := util.RandomString(8)
		bob.SendToDevice(t, "m.room_key_request", alice.UserID, alice.DeviceID, map[string]interface{}{
			"request_id":           reqID,
			"action":               "request",
			"requesting_device_id": "mydevice",
		})
		bob.SendToDevice(t, "m.room_key_request", alice.UserID, alice.DeviceID, map[string]interface{}{
			"request_id":           reqID,
			"action":               "request_cancellation",
			"requesting_device_id": "mydevice",
		})
	}
	bob.SendToDevice(t, "sentinel", alice.UserID, alice.DeviceID, map[string]interface{}{})
	// Loop until we have the sentinel event, the rest should cancel out.
	gotMessages, _ := loopUntilToDeviceEvent(t, alice, nil, "", "sentinel", bob.UserID)
	wantCount := 1
	if count := len(gotMessages); count > wantCount {
		t.Fatalf("expected %d to-device events, got %d : %v", wantCount, count, jsonify(gotMessages))
	}
}

func TestToDeviceDropStaleKeyRequestsStreamNoDelete(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	bob.SendToDevice(t, "m.room_key_request", alice.UserID, alice.DeviceID, map[string]interface{}{
		"request_id":           "A",
		"action":               "request",
		"requesting_device_id": "mydevice",
	})
	msgs1, res := loopUntilToDeviceEvent(t, alice, nil, "", "m.room_key_request", bob.UserID)
	if len(msgs1) != 1 {
		t.Fatalf("got %v want 1 message", jsonify(msgs1))
	}

	// now send a cancellation: we should not delete the cancellation
	bob.SendToDevice(t, "m.room_key_request", alice.UserID, alice.DeviceID, map[string]interface{}{
		"request_id":           "A",
		"action":               "request_cancellation",
		"requesting_device_id": "mydevice",
	})
	time.Sleep(100 * time.Millisecond)
	msgs2, _ := loopUntilToDeviceEvent(t, alice, res, res.Extensions.ToDevice.NextBatch, "m.room_key_request", bob.UserID)
	if len(msgs2) != 1 {
		t.Fatalf("got %v want 1 message", jsonify(msgs2))
	}
	if gjson.ParseBytes(msgs1[0]).Get("content.action").Str != "request" {
		t.Errorf("first message was not action: request: %v", string(msgs1[0]))
	}
	if gjson.ParseBytes(msgs2[0]).Get("content.action").Str != "request_cancellation" {
		t.Errorf("second message was not action: request_cancellation: %v", string(msgs2[0]))
	}
}

func loopUntilToDeviceEvent(t *testing.T, client *CSAPI, res *sync3.Response, since string, wantEvType string, wantSender string) ([]json.RawMessage, *sync3.Response) {
	t.Helper()
	gotEvent := false
	var messages []json.RawMessage
	checkIfHasEvent := func() {
		t.Helper()
		for _, ev := range res.Extensions.ToDevice.Events {
			t.Logf("got to-device: %s", string(ev))
			messages = append(messages, ev)
			evJSON := gjson.ParseBytes(ev)
			if evJSON.Get("type").Str == wantEvType && evJSON.Get("sender").Str == wantSender {
				gotEvent = true
			}
		}
	}
	if res == nil {
		res = client.SlidingSync(t, sync3.Request{
			Extensions: extensions.Request{
				ToDevice: &extensions.ToDeviceRequest{
					Enabled: &boolTrue,
				},
			},
		})
		checkIfHasEvent()
		since = res.Extensions.ToDevice.NextBatch
	}
	waitTime := 10 * time.Second
	start := time.Now()
	for time.Since(start) < waitTime && !gotEvent {
		res = client.SlidingSync(t, sync3.Request{
			Extensions: extensions.Request{
				ToDevice: &extensions.ToDeviceRequest{
					Since: since,
				},
			},
		}, WithPos(res.Pos))
		since = res.Extensions.ToDevice.NextBatch
		checkIfHasEvent()
	}
	if !gotEvent {
		t.Fatalf("did not see to-device message after %v", waitTime)
	}
	return messages, res
}

func jsonify(i interface{}) string {
	b, _ := json.Marshal(i)
	return string(b)
}
