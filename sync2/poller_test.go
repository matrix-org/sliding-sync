package sync2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/rs/zerolog"
)

const initialSinceToken = "0"

// monkey patch out time.Since with a test controlled value.
// This is done in the init block so we can make sure we swap it out BEFORE any pollers
// start. If we wait until pollers exist, we get data races. This includes pollers in tests
// which don't use timeSince, hence the init block.
var (
	timeSinceMu    sync.Mutex
	timeSinceValue = time.Duration(0) // 0 means use the real impl
	timeSleepMu    sync.Mutex
	timeSleepValue = time.Duration(0)  // 0 means use the real impl
	timeSleepCheck func(time.Duration) // called to check sleep values
)

func setTimeSinceValue(val time.Duration) {
	timeSinceMu.Lock()
	defer timeSinceMu.Unlock()
	timeSinceValue = val
}
func setTimeSleepDelay(val time.Duration, fn ...func(d time.Duration)) {
	timeSleepMu.Lock()
	defer timeSleepMu.Unlock()
	timeSleepValue = val
	if len(fn) > 0 {
		timeSleepCheck = fn[0]
	}
}
func init() {
	timeSince = func(t time.Time) time.Duration {
		timeSinceMu.Lock()
		defer timeSinceMu.Unlock()
		if timeSinceValue == 0 {
			return time.Since(t)
		}
		return timeSinceValue
	}
	timeSleep = func(d time.Duration) {
		timeSleepMu.Lock()
		defer timeSleepMu.Unlock()
		if timeSleepCheck != nil {
			timeSleepCheck(d)
		}
		if timeSleepValue == 0 {
			time.Sleep(d)
			return
		}
		time.Sleep(timeSleepValue)
	}
}

// Tests that EnsurePolling works in the happy case
func TestPollerMapEnsurePolling(t *testing.T) {
	nextSince := "next"
	roomID := "!foo:bar"
	roomState := []json.RawMessage{
		json.RawMessage(`{"event":1}`),
		json.RawMessage(`{"event":2}`),
		json.RawMessage(`{"event":3}`),
	}
	initialResponse := &SyncResponse{
		NextBatch: nextSince,
		Rooms: struct {
			Join   map[string]SyncV2JoinResponse   `json:"join"`
			Invite map[string]SyncV2InviteResponse `json:"invite"`
			Leave  map[string]SyncV2LeaveResponse  `json:"leave"`
		}{
			Join: map[string]SyncV2JoinResponse{
				roomID: {
					State: EventsResponse{
						Events: roomState,
					},
				},
			},
		},
	}
	syncRequests := make(chan string)
	syncResponses := make(chan *SyncResponse)
	accumulator, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		syncRequests <- since
		return <-syncResponses, 200, nil
	})
	accumulator.incomingProcess = make(chan struct{})
	accumulator.unblockProcess = make(chan struct{})
	pm := NewPollerMap(client, false)
	pm.SetCallbacks(accumulator)

	ensurePollingUnblocked := make(chan struct{})
	go func() {
		pm.EnsurePolling(PollerID{
			UserID:   "alice:localhost",
			DeviceID: "FOOBAR",
		}, "access_token", "", false, zerolog.New(os.Stderr))
		close(ensurePollingUnblocked)
	}()
	ensureBlocking := func() {
		select {
		case <-ensurePollingUnblocked:
			t.Fatalf("EnsurePolling unblocked")
		default:
		}
	}

	// wait until we get a /sync request
	since := <-syncRequests
	if since != "" {
		t.Fatalf("/sync not made with empty since token, got %v", since)
	}

	// make sure we're still blocking
	ensureBlocking()

	// respond to the /sync request
	syncResponses <- initialResponse

	// make sure we're still blocking
	ensureBlocking()

	// wait until we are processing the state response
	<-accumulator.incomingProcess

	// make sure we're still blocking
	ensureBlocking()

	// finish processing
	accumulator.unblockProcess <- struct{}{}

	// make sure we unblock
	select {
	case <-ensurePollingUnblocked:
	case <-time.After(time.Second):
		t.Fatalf("EnsurePolling did not unblock after 1s")
	}
}

func TestPollerMapEnsurePollingIdempotent(t *testing.T) {
	nextSince := "next"
	roomID := "!foo:bar"
	roomState := []json.RawMessage{
		json.RawMessage(`{"event":1}`),
		json.RawMessage(`{"event":2}`),
		json.RawMessage(`{"event":3}`),
	}
	initialResponse := &SyncResponse{
		NextBatch: nextSince,
		Rooms: struct {
			Join   map[string]SyncV2JoinResponse   `json:"join"`
			Invite map[string]SyncV2InviteResponse `json:"invite"`
			Leave  map[string]SyncV2LeaveResponse  `json:"leave"`
		}{
			Join: map[string]SyncV2JoinResponse{
				roomID: {
					State: EventsResponse{
						Events: roomState,
					},
				},
			},
		},
	}
	syncRequests := make(chan string)
	syncResponses := make(chan *SyncResponse)
	accumulator, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		syncRequests <- since
		return <-syncResponses, 200, nil
	})
	accumulator.incomingProcess = make(chan struct{})
	accumulator.unblockProcess = make(chan struct{})
	pm := NewPollerMap(client, false)
	pm.SetCallbacks(accumulator)

	ensurePollingUnblocked := make(chan struct{})
	var wg sync.WaitGroup
	n := 3
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			t.Logf("EnsurePolling")
			pm.EnsurePolling(PollerID{UserID: "@alice:localhost", DeviceID: "FOOBAR"}, "access_token", "", false, zerolog.New(os.Stderr))
			wg.Done()
			t.Logf("EnsurePolling unblocked")
		}()
	}
	go func() {
		wg.Wait()
		close(ensurePollingUnblocked)
		t.Logf("EnsurePolling all unblocked")
	}()
	ensureBlocking := func() {
		select {
		case <-ensurePollingUnblocked:
			t.Fatalf("EnsurePolling unblocked")
		default:
			t.Logf("EnsurePolling still blocking")
		}
	}

	// wait until we get a /sync request
	since := <-syncRequests
	if since != "" {
		t.Fatalf("/sync not made with empty since token, got %v", since)
	}
	t.Logf("Recv /sync request")

	// make sure we're still blocking
	ensureBlocking()

	// respond to the /sync request
	syncResponses <- initialResponse
	t.Logf("Responded to /sync request")

	// make sure we're still blocking
	ensureBlocking()

	// wait until we are processing the state response
	<-accumulator.incomingProcess
	t.Logf("Processing response...")

	// make sure we're still blocking
	ensureBlocking()

	// finish processing
	accumulator.unblockProcess <- struct{}{}
	t.Logf("Processed response.")

	// make sure we unblock
	select {
	case <-ensurePollingUnblocked:
	case <-time.After(time.Second):
		t.Fatalf("EnsurePolling did not unblock after 1s")
	}
	t.Logf("EnsurePolling unblocked")
}

func TestPollerMapEnsurePollingFailsWithExpiredToken(t *testing.T) {
	accumulator, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		t.Logf("Responding to token '%s' with 401 Unauthorized", authHeader)
		return nil, http.StatusUnauthorized, fmt.Errorf("RUH ROH unrecognised token")
	})
	pm := NewPollerMap(client, false)
	pm.SetCallbacks(accumulator)

	created, err := pm.EnsurePolling(PollerID{}, "dummy_token", "", true, zerolog.New(os.Stderr))

	if created {
		t.Errorf("Expected created=false, got created=true")
	}
	if err == nil {
		t.Errorf("Expected nonnil error, got nil")
	}
}

func TestPollerMap_ExpirePollers(t *testing.T) {
	receiver, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		r := SyncResponse{
			NextBatch: "batchy-mc-batchface",
		}
		return &r, 200, nil
	})
	pm := NewPollerMap(client, false)
	pm.SetCallbacks(receiver)

	// Start 5 pollers.
	pollerSpecs := []struct {
		UserID   string
		DeviceID string
		Token    string
	}{
		{UserID: "alice", DeviceID: "a_device", Token: "a_token"},
		{UserID: "bob", DeviceID: "b_device2", Token: "b_token1"},
		{UserID: "bob", DeviceID: "b_device1", Token: "b_token2"},
		{UserID: "chris", DeviceID: "phone", Token: "c_token"},
		{UserID: "delia", DeviceID: "phone", Token: "d_token"},
	}

	for i, spec := range pollerSpecs {
		created, err := pm.EnsurePolling(
			PollerID{UserID: spec.UserID, DeviceID: spec.DeviceID},
			spec.Token, "", true, logger,
		)
		if err != nil {
			t.Errorf("EnsurePolling error for poller #%d (%v): %s", i, spec, err)
		}
		if !created {
			t.Errorf("Poller #%d (%v) was not newly created", i, spec)
		}
	}

	// Expire some of them. This tests that:
	pm.ExpirePollers([]PollerID{
		// - Easy mode: if you have one poller and ask it to be deleted, it is deleted.
		{"alice", "a_device"},
		// - If you have two devices and ask for one of their pollers to be expired,
		//   only that poller is terminated.
		{"bob", "b_device1"},
		// - If there is a device ID clash, only the specified user's poller is expired.
		//   I.e. Delia unaffected
		{"chris", "phone"},
	})

	// Try to recreate each poller. EnsurePolling should only report having to create a
	// poller for the pollers we asked to be deleted.
	expectDeleted := []bool{
		true,
		false,
		true,
		true,
		false,
	}

	for i, spec := range pollerSpecs {
		created, err := pm.EnsurePolling(
			PollerID{UserID: spec.UserID, DeviceID: spec.DeviceID},
			spec.Token, "", true, logger,
		)
		if err != nil {
			t.Errorf("EnsurePolling error for poller #%d (%v): %s", i, spec, err)
		}
		if created != expectDeleted[i] {
			t.Errorf("Poller #%d (%v): created=%t, expected %t", i, spec, created, expectDeleted[i])
		}
	}
}

// Check that a call to Poll starts polling and accumulating, and terminates on 401s.
func TestPollerPollFromNothing(t *testing.T) {
	nextSince := "next"
	pid := PollerID{UserID: "@alice:localhost", DeviceID: "FOOBAR"}
	roomID := "!foo:bar"
	roomState := []json.RawMessage{
		json.RawMessage(`{"event":1}`),
		json.RawMessage(`{"event":2}`),
		json.RawMessage(`{"event":3}`),
	}
	hasPolledSuccessfully := make(chan struct{})
	accumulator, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		if since == "" {
			var joinResp SyncV2JoinResponse
			joinResp.State.Events = roomState
			return &SyncResponse{
				NextBatch: nextSince,
				Rooms: struct {
					Join   map[string]SyncV2JoinResponse   `json:"join"`
					Invite map[string]SyncV2InviteResponse `json:"invite"`
					Leave  map[string]SyncV2LeaveResponse  `json:"leave"`
				}{
					Join: map[string]SyncV2JoinResponse{
						roomID: joinResp,
					},
				},
			}, 200, nil
		}
		return nil, 401, fmt.Errorf("terminated")
	})
	var wg sync.WaitGroup
	wg.Add(1)
	poller := newPoller(pid, "Authorization: hello world", client, accumulator, zerolog.New(os.Stderr), false)
	go func() {
		defer wg.Done()
		poller.Poll("")
	}()
	go func() {
		poller.WaitUntilInitialSync()
		close(hasPolledSuccessfully)
	}()
	wg.Wait()

	select {
	case <-hasPolledSuccessfully:
		break
	case <-time.After(time.Second):
		t.Errorf("WaitUntilInitialSync failed to fire")
	}
	if len(accumulator.states[roomID]) != len(roomState) {
		t.Errorf("did not accumulate initial state for room, got %d events want %d", len(accumulator.states[roomID]), len(roomState))
	}
	if accumulator.pollerIDToSince[pid] != nextSince {
		t.Errorf("did not persist latest since token, got %s want %s", accumulator.pollerIDToSince[pid], nextSince)
	}
}

// Check that a call to Poll starts polling with an existing since token and accumulates timeline entries
func TestPollerPollFromExisting(t *testing.T) {
	pid := PollerID{UserID: "@alice:localhost", DeviceID: "FOOBAR"}
	roomID := "!foo:bar"
	since := initialSinceToken
	roomTimelineResponses := [][]json.RawMessage{
		{
			json.RawMessage(`{"event":1}`),
			json.RawMessage(`{"event":2}`),
			json.RawMessage(`{"event":3}`),
			json.RawMessage(`{"event":4}`),
		},
		{
			json.RawMessage(`{"event":5}`),
			json.RawMessage(`{"event":6}`),
			json.RawMessage(`{"event":7}`),
		},
		{
			json.RawMessage(`{"event":8}`),
			json.RawMessage(`{"event":9}`),
		},
		{
			json.RawMessage(`{"event":10}`),
		},
	}
	toDeviceResponses := [][]json.RawMessage{
		{}, {}, {}, {json.RawMessage(`{}`)},
	}
	hasPolledSuccessfully := make(chan struct{})
	accumulator, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		if since == "" {
			t.Errorf("called DoSyncV2 with an empty since token")
			return nil, 401, fmt.Errorf("fail")
		}
		sinceInt, err := strconv.Atoi(since)
		if err != nil {
			t.Errorf("called DoSyncV2 with invalid since: %s", since)
			return nil, 401, fmt.Errorf("fail")
		}
		if sinceInt >= len(roomTimelineResponses) {
			return nil, 401, fmt.Errorf("terminated")
		}

		var joinResp SyncV2JoinResponse
		joinResp.Timeline.Events = roomTimelineResponses[sinceInt]
		return &SyncResponse{
			// Add in dummy toDevice messages, so the poller actually persists the since token. (Which
			// it only does for the first poll, after 1min (this test doesn't run that long) OR there are
			// ToDevice messages in the response)
			ToDevice:  EventsResponse{Events: toDeviceResponses[sinceInt]},
			NextBatch: fmt.Sprintf("%d", sinceInt+1),
			Rooms: struct {
				Join   map[string]SyncV2JoinResponse   `json:"join"`
				Invite map[string]SyncV2InviteResponse `json:"invite"`
				Leave  map[string]SyncV2LeaveResponse  `json:"leave"`
			}{
				Join: map[string]SyncV2JoinResponse{
					roomID: joinResp,
				},
			},
		}, 200, nil

	})
	var wg sync.WaitGroup
	wg.Add(1)
	poller := newPoller(pid, "Authorization: hello world", client, accumulator, zerolog.New(os.Stderr), false)
	go func() {
		defer wg.Done()
		poller.Poll(since)
	}()
	go func() {
		poller.WaitUntilInitialSync()
		close(hasPolledSuccessfully)
	}()
	wg.Wait()

	select {
	case <-hasPolledSuccessfully:
		break
	case <-time.After(time.Second):
		t.Errorf("WaitUntilInitialSync failed to fire")
	}
	if len(accumulator.timelines[roomID]) != 10 {
		t.Errorf("did not accumulate timelines for room, got %d events want %d", len(accumulator.timelines[roomID]), 10)
	}
	wantSince := fmt.Sprintf("%d", len(roomTimelineResponses))
	if accumulator.pollerIDToSince[pid] != wantSince {
		t.Errorf("did not persist latest since token, got %s want %s", accumulator.pollerIDToSince[pid], wantSince)
	}
}

// Check that the since token in the database
// 1. is updated if it is the first iteration of poll
// 2. is NOT updated for random events
// 3. is updated if the syncV2 response contains ToDevice messages
// 4. is updated if at least 1min has passed since we last stored a token
func TestPollerPollUpdateDeviceSincePeriodically(t *testing.T) {
	pid := PollerID{UserID: "@alice:localhost", DeviceID: "FOOBAR"}

	syncResponses := make(chan *SyncResponse, 1)
	syncCalledWithSince := make(chan string)
	accumulator, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		if since != "" {
			syncCalledWithSince <- since
		}
		return <-syncResponses, 200, nil
	})
	accumulator.updateSinceCalled = make(chan struct{}, 1)
	poller := newPoller(pid, "Authorization: hello world", client, accumulator, zerolog.New(os.Stderr), false)
	defer poller.Terminate()
	go func() {
		poller.Poll(initialSinceToken)
	}()

	hasPolledSuccessfully := make(chan struct{})

	go func() {
		poller.WaitUntilInitialSync()
		close(hasPolledSuccessfully)
	}()

	// 1. Initial poll updates the database
	next := "1"
	syncResponses <- &SyncResponse{NextBatch: next}
	mustEqualSince(t, <-syncCalledWithSince, initialSinceToken)

	select {
	case <-hasPolledSuccessfully:
		break
	case <-time.After(time.Second):
		t.Errorf("WaitUntilInitialSync failed to fire")
	}
	// Also check that UpdateDeviceSince was called
	select {
	case <-accumulator.updateSinceCalled:
	case <-time.After(time.Millisecond * 100): // give the Poller some time to process the response
		t.Fatalf("did not receive call to UpdateDeviceSince in time")
	}

	if got := accumulator.pollerIDToSince[pid]; got != next {
		t.Fatalf("expected since to be updated to %s, but got %s", next, got)
	}

	// The since token used by calls to doSyncV2
	wantSinceFromSync := next

	// 2. Second request updates the state but NOT the database
	syncResponses <- &SyncResponse{NextBatch: "2"}
	mustEqualSince(t, <-syncCalledWithSince, wantSinceFromSync)

	select {
	case <-accumulator.updateSinceCalled:
		t.Fatalf("unexpected call to UpdateDeviceSince")
	case <-time.After(time.Millisecond * 100):
	}

	if got := accumulator.pollerIDToSince[pid]; got != next {
		t.Fatalf("expected since to be updated to %s, but got %s", next, got)
	}

	// 3. Sync response contains a toDevice message and should be stored in the database
	wantSinceFromSync = "2"
	next = "3"
	syncResponses <- &SyncResponse{
		NextBatch: next,
		ToDevice:  EventsResponse{Events: []json.RawMessage{{}}},
	}
	mustEqualSince(t, <-syncCalledWithSince, wantSinceFromSync)

	select {
	case <-accumulator.updateSinceCalled:
	case <-time.After(time.Millisecond * 100):
		t.Fatalf("did not receive call to UpdateDeviceSince in time")
	}

	if got := accumulator.pollerIDToSince[pid]; got != next {
		t.Fatalf("expected since to be updated to %s, but got %s", wantSinceFromSync, got)
	}
	wantSinceFromSync = next

	// 4. ... some time has passed, this triggers the 1min limit
	setTimeSinceValue(time.Minute * 2)
	defer setTimeSinceValue(0) // reset
	next = "10"
	syncResponses <- &SyncResponse{NextBatch: next}
	mustEqualSince(t, <-syncCalledWithSince, wantSinceFromSync)

	select {
	case <-accumulator.updateSinceCalled:
	case <-time.After(time.Millisecond * 100):
		t.Fatalf("did not receive call to UpdateDeviceSince in time")
	}

	if got := accumulator.pollerIDToSince[pid]; got != next {
		t.Fatalf("expected since to be updated to %s, but got %s", wantSinceFromSync, got)
	}
}

func mustEqualSince(t *testing.T, gotSince, expectedSince string) {
	t.Helper()
	if gotSince != expectedSince {
		t.Fatalf("client.DoSyncV2 using unexpected since token: %s, want %s", gotSince, expectedSince)
	}
}

func TestPollerGivesUpEventually(t *testing.T) {
	deviceID := "FOOBAR"
	hasPolledSuccessfully := make(chan struct{})
	accumulator, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		return nil, 524, fmt.Errorf("gateway timeout")
	})
	// actually sleep to make sure async actions can happen if any
	setTimeSleepDelay(time.Microsecond)
	defer func() { // reset the value after the test runs
		setTimeSleepDelay(0)
	}()
	var wg sync.WaitGroup
	wg.Add(1)
	poller := newPoller(PollerID{UserID: "@alice:localhost", DeviceID: deviceID}, "Authorization: hello world", client, accumulator, zerolog.New(os.Stderr), false)
	go func() {
		defer wg.Done()
		poller.Poll("")
	}()
	go func() {
		poller.WaitUntilInitialSync()
		close(hasPolledSuccessfully)
	}()
	wg.Wait()
	select {
	case <-hasPolledSuccessfully:
	case <-time.After(100 * time.Millisecond):
		break
	}
	// poller should be in the terminated state
	if !poller.terminated.Load() {
		t.Errorf("poller was not terminated")
	}
}

// Tests that the poller backs off in 2,4,8,etc second increments to a variety of errors
func TestPollerBackoff(t *testing.T) {
	deviceID := "FOOBAR"
	hasPolledSuccessfully := make(chan struct{})
	errorResponses := []struct {
		code    int
		backoff time.Duration
		err     error
	}{
		{
			code:    0,
			err:     fmt.Errorf("network error"),
			backoff: 3 * time.Second,
		},
		{
			code:    500,
			err:     fmt.Errorf("internal server error"),
			backoff: 3 * time.Second,
		},
		{
			code:    502,
			err:     fmt.Errorf("bad gateway error"),
			backoff: 3 * time.Second,
		},
		{
			code:    404,
			err:     fmt.Errorf("not found"),
			backoff: 3 * time.Second,
		},
	}
	errorResponsesIndex := 0
	var wantBackoffDuration time.Duration
	accumulator, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		if errorResponsesIndex >= len(errorResponses) {
			return nil, 401, fmt.Errorf("terminated")
		}
		i := errorResponsesIndex
		errorResponsesIndex += 1
		wantBackoffDuration = errorResponses[i].backoff
		return nil, errorResponses[i].code, errorResponses[i].err
	})
	setTimeSleepDelay(time.Millisecond, func(d time.Duration) {
		if d != wantBackoffDuration {
			t.Errorf("time.Sleep called incorrectly: got %v want %v", d, wantBackoffDuration)
		}
	})
	defer func() { // reset the value after the test runs
		setTimeSleepDelay(0)
	}()
	var wg sync.WaitGroup
	wg.Add(1)
	poller := newPoller(PollerID{UserID: "@alice:localhost", DeviceID: deviceID}, "Authorization: hello world", client, accumulator, zerolog.New(os.Stderr), false)
	go func() {
		defer wg.Done()
		poller.Poll("some_since_value")
	}()
	go func() {
		poller.WaitUntilInitialSync()
		close(hasPolledSuccessfully)
	}()
	wg.Wait()
	select {
	case <-hasPolledSuccessfully:
	case <-time.After(100 * time.Millisecond):
		break
	}
	if errorResponsesIndex != len(errorResponses) {
		t.Errorf("did not call DoSyncV2 enough, got %d times, want %d", errorResponsesIndex+1, len(errorResponses))
	}
}

// Regression test to make sure that if you start polling with an invalid token, we do end up unblocking WaitUntilInitialSync
// and don't end up blocking forever.
func TestPollerUnblocksIfTerminatedInitially(t *testing.T) {
	deviceID := "FOOBAR"
	accumulator, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		return nil, 401, fmt.Errorf("terminated")
	})

	pollUnblocked := make(chan struct{})
	waitUntilInitialSyncUnblocked := make(chan struct{})
	poller := newPoller(PollerID{UserID: "@alice:localhost", DeviceID: deviceID}, "Authorization: hello world", client, accumulator, zerolog.New(os.Stderr), false)
	go func() {
		poller.Poll("")
		close(pollUnblocked)
	}()
	go func() {
		poller.WaitUntilInitialSync()
		close(waitUntilInitialSyncUnblocked)
	}()

	select {
	case <-pollUnblocked:
		break
	case <-time.After(time.Second):
		t.Errorf("Poll() did not unblock")
	}

	select {
	case <-waitUntilInitialSyncUnblocked:
		break
	case <-time.After(time.Second):
		t.Errorf("WaitUntilInitialSync() did not unblock")
	}
}

// Test that the poller sends the same sync v2 request, without incrementing the since token,
// when an errorable callback returns an error.
func TestPollerResendsOnCallbackError(t *testing.T) {
	pid := PollerID{UserID: "@TestPollerResendsOnCallbackError:localhost", DeviceID: "FOOBAR"}

	defer func() { // reset the value after the test runs
		setTimeSleepDelay(0)
	}()
	// we don't actually want to wait 3s between retries, so monkey patch it out
	setTimeSleepDelay(time.Millisecond)

	testCases := []struct {
		name             string
		generateReceiver func() V2DataReceiver
		syncResponse     *SyncResponse
	}{
		{
			name: "AddToDeviceMessages",
			// generate a response which will trigger the right callback
			syncResponse: &SyncResponse{
				ToDevice: EventsResponse{
					Events: []json.RawMessage{
						[]byte(`{"type":"device","content":{"yep":true}}`),
					},
				},
			},
			// generate a receiver which errors for the right callback
			generateReceiver: func() V2DataReceiver {
				return &overrideDataReceiver{
					addToDeviceMessages: func(ctx context.Context, userID, deviceID string, msgs []json.RawMessage) error {
						return fmt.Errorf("addToDeviceMessages error")
					},
				}
			},
		},
		{
			name: "OnE2EEData,OTKCount",
			// generate a response which will trigger the right callback
			syncResponse: &SyncResponse{
				DeviceListsOTKCount: map[string]int{
					"foo": 5,
				},
			},
			// generate a receiver which errors for the right callback
			generateReceiver: func() V2DataReceiver {
				return &overrideDataReceiver{
					onE2EEData: func(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) error {
						return fmt.Errorf("onE2EEData error")
					},
				}
			},
		},
		{
			name: "OnE2EEData,FallbackKeys",
			// generate a response which will trigger the right callback
			syncResponse: &SyncResponse{
				DeviceUnusedFallbackKeyTypes: []string{"foo", "bar"},
			},
			// generate a receiver which errors for the right callback
			generateReceiver: func() V2DataReceiver {
				return &overrideDataReceiver{
					onE2EEData: func(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) error {
						return fmt.Errorf("onE2EEData error")
					},
				}
			},
		},
		{
			name: "OnE2EEData,DeviceListChanges",
			// generate a response which will trigger the right callback
			syncResponse: &SyncResponse{
				DeviceLists: struct {
					Changed []string `json:"changed,omitempty"`
					Left    []string `json:"left,omitempty"`
				}{
					Changed: []string{"alice"},
					Left:    []string{"bob"},
				},
			},
			// generate a receiver which errors for the right callback
			generateReceiver: func() V2DataReceiver {
				return &overrideDataReceiver{
					onE2EEData: func(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) error {
						return fmt.Errorf("onE2EEData error")
					},
				}
			},
		},
		{
			name: "Initialise",
			// generate a response which will trigger the right callback
			syncResponse: &SyncResponse{
				Rooms: SyncRoomsResponse{
					Join: map[string]SyncV2JoinResponse{
						"!foo:bar": {
							State: EventsResponse{
								Events: []json.RawMessage{
									[]byte(`{"type":"m.room.create","state_key":"","content":{},"sender":"@alice:localhost","event_id":"$111"}`),
								},
							},
						},
					},
				},
			},
			// generate a receiver which errors for the right callback
			generateReceiver: func() V2DataReceiver {
				return &overrideDataReceiver{
					initialise: func(ctx context.Context, roomID string, state []json.RawMessage) error {
						return fmt.Errorf("initialise error")
					},
				}
			},
		},
		{
			name: "Accumulate",
			// generate a response which will trigger the right callback
			syncResponse: &SyncResponse{
				Rooms: SyncRoomsResponse{
					Join: map[string]SyncV2JoinResponse{
						"!foo:bar": {
							Timeline: TimelineResponse{
								Events: []json.RawMessage{
									[]byte(`{"type":"m.room.message","content":{},"sender":"@alice:localhost","event_id":"$222"}`),
								},
							},
						},
					},
				},
			},
			// generate a receiver which errors for the right callback
			generateReceiver: func() V2DataReceiver {
				return &overrideDataReceiver{
					accumulate: func(ctx context.Context, userID, deviceID, roomID, prevBatch string, timeline []json.RawMessage) error {
						return fmt.Errorf("accumulate error")
					},
				}
			},
		},
		{
			name: "OnAccountData,Global",
			// generate a response which will trigger the right callback
			syncResponse: &SyncResponse{
				AccountData: EventsResponse{
					Events: []json.RawMessage{
						[]byte(`{"type":"foo","content":{"bar":53}}`),
					},
				},
			},
			// generate a receiver which errors for the right callback
			generateReceiver: func() V2DataReceiver {
				return &overrideDataReceiver{
					onAccountData: func(ctx context.Context, userID, roomID string, events []json.RawMessage) error {
						return fmt.Errorf("onAccountData error")
					},
				}
			},
		},
		{
			name: "OnAccountData,Room",
			// generate a response which will trigger the right callback
			syncResponse: &SyncResponse{
				Rooms: SyncRoomsResponse{
					Join: map[string]SyncV2JoinResponse{
						"!foo:bar": {
							AccountData: EventsResponse{
								Events: []json.RawMessage{
									[]byte(`{"type":"foo_room","content":{"bar":53}}`),
								},
							},
						},
					},
				},
			},
			// generate a receiver which errors for the right callback
			generateReceiver: func() V2DataReceiver {
				return &overrideDataReceiver{
					onAccountData: func(ctx context.Context, userID, roomID string, events []json.RawMessage) error {
						return fmt.Errorf("onAccountData error")
					},
				}
			},
		},
		{
			name: "OnInvite",
			// generate a response which will trigger the right callback
			syncResponse: &SyncResponse{
				Rooms: SyncRoomsResponse{
					Invite: map[string]SyncV2InviteResponse{
						"!foo:bar": {
							InviteState: EventsResponse{
								Events: []json.RawMessage{
									[]byte(`{"type":"foo_room","content":{"bar":53}}`),
								},
							},
						},
					},
				},
			},
			// generate a receiver which errors for the right callback
			generateReceiver: func() V2DataReceiver {
				return &overrideDataReceiver{
					onInvite: func(ctx context.Context, userID, roomID string, inviteState []json.RawMessage) error {
						return fmt.Errorf("onInvite error")
					},
				}
			},
		},
		{
			name: "OnLeftRoom",
			// generate a response which will trigger the right callback
			syncResponse: &SyncResponse{
				Rooms: SyncRoomsResponse{
					Leave: map[string]SyncV2LeaveResponse{
						"!foo:bar": {
							Timeline: TimelineResponse{
								Events: []json.RawMessage{
									[]byte(`{"type":"m.room.member","state_key":"` + pid.UserID + `","content":{"membership":"leave"}}`),
								},
							},
						},
					},
				},
			},
			// generate a receiver which errors for the right callback
			generateReceiver: func() V2DataReceiver {
				return &overrideDataReceiver{
					onLeftRoom: func(ctx context.Context, userID, roomID string, leaveEvent json.RawMessage) error {
						return fmt.Errorf("onLeftRoom error")
					},
				}
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		// test that if you get an error NOT on an initial sync, we retry with the right since token
		var numSyncLoops int
		waitForStuckPolling := make(chan struct{})
		client := &mockClient{
			// We initially sync with `initialSinceToken`, then advance the client to "1".
			// We then expect the client to get stuck with "1" and never send "2", because
			// the error returned in the receiver is preventing the advancement.
			fn: func(authHeader, since string) (*SyncResponse, int, error) {
				t.Logf("%v DoSyncV2 since=%v", tc.name, since)
				if since == initialSinceToken {
					return &SyncResponse{
						NextBatch: "1",
					}, 200, nil
				}
				if since != "1" {
					t.Errorf("bad since token, got %s", since)
					close(waitForStuckPolling)
					return nil, 0, fmt.Errorf("bad since token: %v", since)
				}
				numSyncLoops++
				if numSyncLoops > 2 {
					// we are clearly stuck hitting "1" over and over, which is good.
					// close the channel to unblock this test.
					t.Log("stuck in a loop: closing channel")
					close(waitForStuckPolling)
				}
				// we should never sync with since="2" because this syncResponse is going to
				// produce an error in the receiver
				tc.syncResponse.NextBatch = "2"
				// we expect the since token to be "1"
				return tc.syncResponse, 200, nil
			},
		}
		receiver := tc.generateReceiver()
		poller := newPoller(pid, "Authorization: hello world", client, receiver, zerolog.New(os.Stderr), false)
		waitForInitialSync(t, poller)
		select {
		case <-waitForStuckPolling:
			poller.Terminate()
			continue
		case <-time.After(time.Second):
			t.Fatalf("%s: timed out waiting for repeated polling", tc.name)
		}
	}
}

// The purpose of this test is to make sure *internal.DataError errors do NOT cause the since token
// to be retried.
func TestPollerDoesNotResendOnDataError(t *testing.T) {
	pid := PollerID{UserID: "@TestPollerDoesNotResendOnDataError:localhost", DeviceID: "FOOBAR"}
	// make a receiver which will return a DataError when Accumulate is called
	receiver := &overrideDataReceiver{
		accumulate: func(ctx context.Context, userID, deviceID, roomID, prevBatch string, timeline []json.RawMessage) error {
			return internal.NewDataError("this is a test: %v", 42)
		},
	}
	waitForSuccess := make(chan struct{})
	lastSince := ""
	client := &mockClient{
		// Process the initial sync then send back a timeline for a room which will cause Accumulate to be called.
		// This should return a DataError but the since token will still be advanced due to it being a DataError
		// and not some other kind of error.
		fn: func(authHeader, since string) (*SyncResponse, int, error) {
			if since == "" {
				// we should start with since=0, but can end up with since=""
				// if we continue to advance beyond what we are testing.
				return nil, 500, fmt.Errorf("test has ended")
			}
			t.Logf("DoSyncV2 since=%v, last=%v", since, lastSince)
			if lastSince == since {
				t.Errorf("since token was retried: got %v", since)
			}
			lastSince = since
			switch since {
			case initialSinceToken:
				// skip over initial syncs
				return &SyncResponse{
					NextBatch: "1",
				}, 200, nil
			case "1":
				// return a response which will trigger Accumulate code
				return &SyncResponse{
					NextBatch: "2",
					Rooms: SyncRoomsResponse{
						Join: map[string]SyncV2JoinResponse{
							"!foo:bar": {
								Timeline: TimelineResponse{
									Events: []json.RawMessage{
										[]byte(`{"type":"m.room.message","content":{},"sender":"@alice:localhost","event_id":"$222"}`),
									},
								},
							},
						},
					},
				}, 200, nil
			case "2":
				close(waitForSuccess)
				return &SyncResponse{
					NextBatch: "3",
				}, 200, nil
			}
			return &SyncResponse{
				NextBatch: "",
			}, 200, nil
		},
	}
	poller := newPoller(pid, "Authorization: hello world", client, receiver, zerolog.New(os.Stderr), false)
	waitForInitialSync(t, poller)
	select {
	case <-waitForSuccess:
	case <-time.After(time.Second):
		t.Errorf("timed out waiting for repeated polling")
	}
	poller.Terminate()
}

// The purpose of this test is to make sure we don't incorrectly skip retrying when a v2 response has many errors,
// some of which are retriable and some of which are not.
func TestPollerResendsOnDataErrorWithOtherErrors(t *testing.T) {
	pid := PollerID{UserID: "@TestPollerResendsOnDataErrorWithOtherErrors:localhost", DeviceID: "FOOBAR"}
	dontRetryRoomID := "!dont-retry:localhost"
	// make a receiver which will return a DataError when Accumulate is called
	receiver := &overrideDataReceiver{
		accumulate: func(ctx context.Context, userID, deviceID, roomID, prevBatch string, timeline []json.RawMessage) error {
			if roomID == dontRetryRoomID {
				return internal.NewDataError("accumulate this is a test: %v", 42)
			}
			return fmt.Errorf("accumulate retriable error")
		},
		onAccountData: func(ctx context.Context, userID, roomID string, events []json.RawMessage) error {
			return fmt.Errorf("onAccountData retriable error")
		},
		onLeftRoom: func(ctx context.Context, userID, roomID string, leaveEvent json.RawMessage) error {
			return internal.NewDataError("onLeftRoom this is a test: %v", 42)
		},
	}
	poller := newPoller(pid, "Authorization: hello world", nil, receiver, zerolog.New(os.Stderr), false)
	testCases := []struct {
		name      string
		res       SyncResponse
		wantRetry bool
	}{
		{
			name: "single unretriable error",
			res: SyncResponse{
				NextBatch: "2",
				Rooms: SyncRoomsResponse{
					Join: map[string]SyncV2JoinResponse{
						dontRetryRoomID: {
							Timeline: TimelineResponse{
								Events: []json.RawMessage{
									testutils.NewMessageEvent(t, pid.UserID, "Don't Retry Me!"),
								},
							},
						},
					},
				},
			},
			wantRetry: false,
		},
		{
			name: "single retriable error",
			res: SyncResponse{
				NextBatch: "2",
				Rooms: SyncRoomsResponse{
					Join: map[string]SyncV2JoinResponse{
						"!retry:localhost": {
							AccountData: EventsResponse{
								Events: []json.RawMessage{
									testutils.NewAccountData(t, "m.retry", map[string]interface{}{}),
								},
							},
						},
					},
				},
			},
			wantRetry: true,
		},
		{
			name: "1 retriable error, 1 unretriable error",
			res: SyncResponse{
				NextBatch: "2",
				Rooms: SyncRoomsResponse{
					Join: map[string]SyncV2JoinResponse{
						"!retry:localhost": {
							AccountData: EventsResponse{
								Events: []json.RawMessage{
									testutils.NewAccountData(t, "m.retry", map[string]interface{}{}),
								},
							},
						},
						dontRetryRoomID: {
							Timeline: TimelineResponse{
								Events: []json.RawMessage{
									testutils.NewMessageEvent(t, pid.UserID, "Don't Retry Me!"),
								},
							},
						},
					},
				},
			},
			wantRetry: true,
		},
		{
			name: "2 unretriable errors",
			res: SyncResponse{
				NextBatch: "2",
				Rooms: SyncRoomsResponse{
					Join: map[string]SyncV2JoinResponse{
						dontRetryRoomID: {
							Timeline: TimelineResponse{
								Events: []json.RawMessage{
									testutils.NewMessageEvent(t, pid.UserID, "Don't Retry Me!"),
								},
							},
						},
					},
					Leave: map[string]SyncV2LeaveResponse{
						dontRetryRoomID: {
							Timeline: TimelineResponse{
								Events: []json.RawMessage{
									testutils.NewMessageEvent(t, pid.UserID, "Don't Retry Me!"),
								},
							},
						},
					},
				},
			},
			wantRetry: false,
		},
		{
			// sanity to make sure the 2x unretriable are both independently unretriable
			name: "another 1 unretriable error",
			res: SyncResponse{
				NextBatch: "2",
				Rooms: SyncRoomsResponse{
					Leave: map[string]SyncV2LeaveResponse{
						dontRetryRoomID: {
							Timeline: TimelineResponse{
								Events: []json.RawMessage{
									testutils.NewMessageEvent(t, pid.UserID, "Don't Retry Me!"),
								},
							},
						},
					},
				},
			},
			wantRetry: false,
		},
	}
	// rather than set up the entire loop and machinery, just directly call parseRoomsResponse with various failure modes
	for _, tc := range testCases {
		err := poller.parseRoomsResponse(context.Background(), &tc.res)
		if err == nil {
			t.Errorf("%s: got no error", tc.name)
			continue
		}
		t.Logf(tc.name, err)
		got := shouldRetry(err)
		if got != tc.wantRetry {
			t.Errorf("%s: got retry %v want %v", tc.name, got, tc.wantRetry)
		}
	}
}

func waitForInitialSync(t *testing.T, poller *poller) {
	go func() {
		poller.Poll(initialSinceToken)
	}()

	hasPolledSuccessfully := make(chan struct{})

	go func() {
		poller.WaitUntilInitialSync()
		close(hasPolledSuccessfully)
	}()

	select {
	case <-hasPolledSuccessfully:
		return
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for WaitUntilInitialSync to return")
	}
}

type mockClient struct {
	fn func(authHeader, since string) (*SyncResponse, int, error)
}

func (c *mockClient) Versions(ctx context.Context) ([]string, error) {
	return []string{"v1.1"}, nil
}
func (c *mockClient) DoSyncV2(ctx context.Context, authHeader, since string, isFirst, toDeviceOnly bool) (*SyncResponse, int, error) {
	return c.fn(authHeader, since)
}
func (c *mockClient) WhoAmI(ctx context.Context, authHeader string) (string, string, error) {
	return "@alice:localhost", "device_123", nil
}

type mockDataReceiver struct {
	*overrideDataReceiver
	mu                *sync.Mutex
	states            map[string][]json.RawMessage
	timelines         map[string][]json.RawMessage
	pollerIDToSince   map[PollerID]string
	incomingProcess   chan struct{}
	unblockProcess    chan struct{}
	updateSinceCalled chan struct{}
}

func (a *mockDataReceiver) Accumulate(ctx context.Context, userID, deviceID, roomID string, timeline TimelineResponse) error {
	a.timelines[roomID] = append(a.timelines[roomID], timeline.Events...)
	return nil
}
func (a *mockDataReceiver) Initialise(ctx context.Context, roomID string, state []json.RawMessage) error {
	a.states[roomID] = state
	if a.incomingProcess != nil {
		a.incomingProcess <- struct{}{}
	}
	if a.unblockProcess != nil {
		<-a.unblockProcess
	}
	// The return value is a list of unknown state events to be prepended to the room
	// timeline. Untested here---return nil for now.
	return nil
}
func (s *mockDataReceiver) UpdateDeviceSince(ctx context.Context, userID, deviceID, since string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pollerIDToSince[PollerID{UserID: userID, DeviceID: deviceID}] = since
	if s.updateSinceCalled != nil {
		s.updateSinceCalled <- struct{}{}
	}
}

type overrideDataReceiver struct {
	accumulate          func(ctx context.Context, userID, deviceID, roomID, prevBatch string, timeline []json.RawMessage) error
	initialise          func(ctx context.Context, roomID string, state []json.RawMessage) error
	setTyping           func(ctx context.Context, pollerID PollerID, roomID string, ephEvent json.RawMessage)
	updateDeviceSince   func(ctx context.Context, userID, deviceID, since string)
	addToDeviceMessages func(ctx context.Context, userID, deviceID string, msgs []json.RawMessage) error
	updateUnreadCounts  func(ctx context.Context, roomID, userID string, highlightCount, notifCount *int)
	onAccountData       func(ctx context.Context, userID, roomID string, events []json.RawMessage) error
	onReceipt           func(ctx context.Context, userID, roomID, ephEventType string, ephEvent json.RawMessage)
	onInvite            func(ctx context.Context, userID, roomID string, inviteState []json.RawMessage) error
	onLeftRoom          func(ctx context.Context, userID, roomID string, leaveEvent json.RawMessage) error
	onE2EEData          func(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) error
	onTerminated        func(ctx context.Context, pollerID PollerID)
	onExpiredToken      func(ctx context.Context, accessTokenHash, userID, deviceID string)
}

func (s *overrideDataReceiver) Accumulate(ctx context.Context, userID, deviceID, roomID string, timeline TimelineResponse) error {
	if s.accumulate == nil {
		return nil
	}
	return s.accumulate(ctx, userID, deviceID, roomID, timeline.PrevBatch, timeline.Events)
}
func (s *overrideDataReceiver) Initialise(ctx context.Context, roomID string, state []json.RawMessage) error {
	if s.initialise == nil {
		return nil
	}
	return s.initialise(ctx, roomID, state)
}
func (s *overrideDataReceiver) SetTyping(ctx context.Context, pollerID PollerID, roomID string, ephEvent json.RawMessage) {
	if s.setTyping == nil {
		return
	}
	s.setTyping(ctx, pollerID, roomID, ephEvent)
}
func (s *overrideDataReceiver) UpdateDeviceSince(ctx context.Context, userID, deviceID, since string) {
	if s.updateDeviceSince == nil {
		return
	}
	s.updateDeviceSince(ctx, userID, deviceID, since)
}
func (s *overrideDataReceiver) AddToDeviceMessages(ctx context.Context, userID, deviceID string, msgs []json.RawMessage) error {
	if s.addToDeviceMessages == nil {
		return nil
	}
	return s.addToDeviceMessages(ctx, userID, deviceID, msgs)
}
func (s *overrideDataReceiver) UpdateUnreadCounts(ctx context.Context, roomID, userID string, highlightCount, notifCount *int) {
	if s.updateUnreadCounts == nil {
		return
	}
	s.updateUnreadCounts(ctx, roomID, userID, highlightCount, notifCount)
}
func (s *overrideDataReceiver) OnAccountData(ctx context.Context, userID, roomID string, events []json.RawMessage) error {
	if s.onAccountData == nil {
		return nil
	}
	return s.onAccountData(ctx, userID, roomID, events)
}
func (s *overrideDataReceiver) OnReceipt(ctx context.Context, userID, roomID, ephEventType string, ephEvent json.RawMessage) {
	if s.onReceipt == nil {
		return
	}
	s.onReceipt(ctx, userID, roomID, ephEventType, ephEvent)
}
func (s *overrideDataReceiver) OnInvite(ctx context.Context, userID, roomID string, inviteState []json.RawMessage) error {
	if s.onInvite == nil {
		return nil
	}
	return s.onInvite(ctx, userID, roomID, inviteState)
}
func (s *overrideDataReceiver) OnLeftRoom(ctx context.Context, userID, roomID string, leaveEvent json.RawMessage) error {
	if s.onLeftRoom == nil {
		return nil
	}
	return s.onLeftRoom(ctx, userID, roomID, leaveEvent)
}
func (s *overrideDataReceiver) OnE2EEData(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) error {
	if s.onE2EEData == nil {
		return nil
	}
	return s.onE2EEData(ctx, userID, deviceID, otkCounts, fallbackKeyTypes, deviceListChanges)
}
func (s *overrideDataReceiver) OnTerminated(ctx context.Context, pollerID PollerID) {
	if s.onTerminated == nil {
		return
	}
	s.onTerminated(ctx, pollerID)
}
func (s *overrideDataReceiver) OnExpiredToken(ctx context.Context, accessTokenHash, userID, deviceID string) {
	if s.onExpiredToken == nil {
		return
	}
	s.onExpiredToken(ctx, accessTokenHash, userID, deviceID)
}

func newMocks(doSyncV2 func(authHeader, since string) (*SyncResponse, int, error)) (*mockDataReceiver, *mockClient) {
	client := &mockClient{
		fn: doSyncV2,
	}
	accumulator := &mockDataReceiver{
		overrideDataReceiver: &overrideDataReceiver{},
		mu:                   &sync.Mutex{},
		states:               make(map[string][]json.RawMessage),
		timelines:            make(map[string][]json.RawMessage),
		pollerIDToSince:      make(map[PollerID]string),
	}
	return accumulator, client
}
