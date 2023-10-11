package sync2

import (
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/pubsub"
)

type syncSlice[T any] struct {
	slice []T
	mu    sync.Mutex
}

func (s *syncSlice[T]) append(item T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slice = append(s.slice, item)
}

func (s *syncSlice[T]) clone() []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]T, len(s.slice))
	copy(result, s.slice)
	return result
}

func TestDeviceTickerBasic(t *testing.T) {
	duration := time.Millisecond
	ticker := NewDeviceDataTicker(duration)
	var payloads syncSlice[*pubsub.V2DeviceData]
	ticker.SetCallback(func(payload *pubsub.V2DeviceData) {
		payloads.append(payload)
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
	result := payloads.clone()
	if len(result) != 1 {
		t.Fatalf("expected 1 callback, got %d", len(result))
	}
	want := map[string][]string{
		"a": {"b"},
	}
	assertPayloadEqual(t, result[0].UserIDToDeviceIDs, want)
	// check stopping works
	payloads = syncSlice[*pubsub.V2DeviceData]{}
	ticker.Stop()
	wg.Wait()
	time.Sleep(duration * 2)
	result = payloads.clone()
	if len(result) != 0 {
		t.Fatalf("got extra payloads: %+v", result)
	}
}

func TestDeviceTickerBatchesCorrectly(t *testing.T) {
	duration := 100 * time.Millisecond
	ticker := NewDeviceDataTicker(duration)
	var payloads syncSlice[*pubsub.V2DeviceData]
	ticker.SetCallback(func(payload *pubsub.V2DeviceData) {
		payloads.append(payload)
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
	result := payloads.clone()
	if len(result) != 1 {
		t.Fatalf("expected 1 callback, got %d", len(result))
	}
	want := map[string][]string{
		"a": {"b", "bb"},
		"x": {"y"},
	}
	assertPayloadEqual(t, result[0].UserIDToDeviceIDs, want)
}

func TestDeviceTickerForgetsAfterEmitting(t *testing.T) {
	duration := time.Millisecond
	ticker := NewDeviceDataTicker(duration)
	var payloads syncSlice[*pubsub.V2DeviceData]
	ticker.SetCallback(func(payload *pubsub.V2DeviceData) {
		payloads.append(payload)
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
	result := payloads.clone()
	if len(result) != 1 {
		t.Fatalf("got %d payloads, want 1", len(result))
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
