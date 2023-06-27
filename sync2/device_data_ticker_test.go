package sync2

import (
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/pubsub"
)

func TestDeviceTickerBasic(t *testing.T) {
	duration := time.Millisecond
	ticker := NewDeviceDataTicker(duration)
	var payloads []*pubsub.V2DeviceData
	ticker.SetCallback(func(payload *pubsub.V2DeviceData) {
		payloads = append(payloads, payload)
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		t.Log("starting the ticker")
		ticker.Run()
		wg.Done()
	}()
	time.Sleep(duration * 2) // wait until the ticker is consuming
	t.Log("remembering a poller")
	ticker.Remember(PollerID{
		UserID:   "a",
		DeviceID: "b",
	})
	time.Sleep(duration * 2)
	if len(payloads) != 1 {
		t.Fatalf("expected 1 callback, got %d", len(payloads))
	}
	want := map[string][]string{
		"a": {"b"},
	}
	assertPayloadEqual(t, payloads[0].UserIDToDeviceIDs, want)
	// check stopping works
	payloads = []*pubsub.V2DeviceData{}
	ticker.Stop()
	wg.Wait()
	time.Sleep(duration * 2)
	if len(payloads) != 0 {
		t.Fatalf("got extra payloads: %+v", payloads)
	}
}

func TestDeviceTickerBatchesCorrectly(t *testing.T) {
	duration := 100 * time.Millisecond
	ticker := NewDeviceDataTicker(duration)
	var payloads []*pubsub.V2DeviceData
	ticker.SetCallback(func(payload *pubsub.V2DeviceData) {
		payloads = append(payloads, payload)
	})
	go ticker.Run()
	defer ticker.Stop()
	ticker.Remember(PollerID{
		UserID:   "a",
		DeviceID: "b",
	})
	ticker.Remember(PollerID{
		UserID:   "a",
		DeviceID: "bb", // different device, same user
	})
	ticker.Remember(PollerID{
		UserID:   "a",
		DeviceID: "b", // dupe poller ID
	})
	ticker.Remember(PollerID{
		UserID:   "x",
		DeviceID: "y", // new device and user
	})
	time.Sleep(duration * 2)
	if len(payloads) != 1 {
		t.Fatalf("expected 1 callback, got %d", len(payloads))
	}
	want := map[string][]string{
		"a": {"b", "bb"},
		"x": {"y"},
	}
	assertPayloadEqual(t, payloads[0].UserIDToDeviceIDs, want)
}

func TestDeviceTickerForgetsAfterEmitting(t *testing.T) {
	duration := time.Millisecond
	ticker := NewDeviceDataTicker(duration)
	var payloads []*pubsub.V2DeviceData

	ticker.SetCallback(func(payload *pubsub.V2DeviceData) {
		payloads = append(payloads, payload)
	})
	ticker.Remember(PollerID{
		UserID:   "a",
		DeviceID: "b",
	})

	go ticker.Run()
	defer ticker.Stop()
	ticker.Remember(PollerID{
		UserID:   "a",
		DeviceID: "b",
	})
	time.Sleep(10 * duration)
	if len(payloads) != 1 {
		t.Fatalf("got %d payloads, want 1", len(payloads))
	}
}

func assertPayloadEqual(t *testing.T, got, want map[string][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %+v\nwant %+v\n", got, want)
	}
	for userID, wantDeviceIDs := range want {
		gotDeviceIDs := got[userID]
		sort.Strings(wantDeviceIDs)
		sort.Strings(gotDeviceIDs)
		if !reflect.DeepEqual(gotDeviceIDs, wantDeviceIDs) {
			t.Errorf("user %v got devices %v want %v", userID, gotDeviceIDs, wantDeviceIDs)
		}
	}
}
