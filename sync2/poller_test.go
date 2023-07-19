package sync2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

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
	since := "0"
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
	accumulator, client := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
		return <-syncResponses, 200, nil
	})
	accumulator.updateSinceCalled = make(chan struct{}, 1)
	poller := newPoller(pid, "Authorization: hello world", client, accumulator, zerolog.New(os.Stderr), false)
	defer poller.Terminate()
	go func() {
		poller.Poll("")
	}()

	hasPolledSuccessfully := make(chan struct{})

	go func() {
		poller.WaitUntilInitialSync()
		close(hasPolledSuccessfully)
	}()

	// 1. Initial poll updates the database
	wantSince := "1"
	syncResponses <- &SyncResponse{NextBatch: wantSince}

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

	if got := accumulator.pollerIDToSince[pid]; got != wantSince {
		t.Fatalf("expected since to be updated to %s, but got %s", wantSince, got)
	}

	// 2. Second request updates the state but NOT the database
	next := "2"
	syncResponses <- &SyncResponse{NextBatch: next}
	if got := accumulator.pollerIDToSince[pid]; got != wantSince {
		t.Fatalf("expected since to be updated to %s, but got %s", wantSince, got)
	}

	select {
	case <-accumulator.updateSinceCalled:
		t.Fatalf("unexpected call to UpdateDeviceSince")
	case <-time.After(time.Millisecond * 100):
	}

	// 3. Sync response contains a toDevice message and should be stored in the database
	next = "3"
	wantSince = "3"
	syncResponses <- &SyncResponse{
		NextBatch: next,
		ToDevice:  EventsResponse{Events: []json.RawMessage{{}}},
	}
	select {
	case <-accumulator.updateSinceCalled:
	case <-time.After(time.Millisecond * 100):
		t.Fatalf("did not receive call to UpdateDeviceSince in time")
	}

	if got := accumulator.pollerIDToSince[pid]; got != wantSince {
		t.Fatalf("expected since to be updated to %s, but got %s", wantSince, got)
	}

	// 4. ... some time has passed, this triggers the 1min limit
	timeSince = func(d time.Time) time.Duration {
		return time.Minute * 2
	}
	next = "10"
	wantSince = "10"
	syncResponses <- &SyncResponse{NextBatch: next}
	select {
	case <-accumulator.updateSinceCalled:
	case <-time.After(time.Millisecond * 100):
		t.Fatalf("did not receive call to UpdateDeviceSince in time")
	}

	if got := accumulator.pollerIDToSince[pid]; got != wantSince {
		t.Fatalf("expected since to be updated to %s, but got %s", wantSince, got)
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
	timeSleep = func(d time.Duration) {
		if d != wantBackoffDuration {
			t.Errorf("time.Sleep called incorrectly: got %v want %v", d, wantBackoffDuration)
		}
		// actually sleep to make sure async actions can happen if any
		time.Sleep(1 * time.Millisecond)
	}
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

type mockClient struct {
	fn func(authHeader, since string) (*SyncResponse, int, error)
}

func (c *mockClient) DoSyncV2(ctx context.Context, authHeader, since string, isFirst, toDeviceOnly bool) (*SyncResponse, int, error) {
	return c.fn(authHeader, since)
}
func (c *mockClient) WhoAmI(authHeader string) (string, string, error) {
	return "@alice:localhost", "device_123", nil
}

type mockDataReceiver struct {
	states            map[string][]json.RawMessage
	timelines         map[string][]json.RawMessage
	pollerIDToSince   map[PollerID]string
	incomingProcess   chan struct{}
	unblockProcess    chan struct{}
	updateSinceCalled chan struct{}
}

func (a *mockDataReceiver) Accumulate(ctx context.Context, userID, deviceID, roomID, prevBatch string, timeline []json.RawMessage) {
	a.timelines[roomID] = append(a.timelines[roomID], timeline...)
}
func (a *mockDataReceiver) Initialise(ctx context.Context, roomID string, state []json.RawMessage) []json.RawMessage {
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
func (a *mockDataReceiver) SetTyping(ctx context.Context, roomID string, ephEvent json.RawMessage) {
}
func (s *mockDataReceiver) UpdateDeviceSince(ctx context.Context, userID, deviceID, since string) {
	s.pollerIDToSince[PollerID{UserID: userID, DeviceID: deviceID}] = since
	if s.updateSinceCalled != nil {
		s.updateSinceCalled <- struct{}{}
	}
}
func (s *mockDataReceiver) AddToDeviceMessages(ctx context.Context, userID, deviceID string, msgs []json.RawMessage) {
}

func (s *mockDataReceiver) UpdateUnreadCounts(ctx context.Context, roomID, userID string, highlightCount, notifCount *int) {
}
func (s *mockDataReceiver) OnAccountData(ctx context.Context, userID, roomID string, events []json.RawMessage) {
}
func (s *mockDataReceiver) OnReceipt(ctx context.Context, userID, roomID, ephEventType string, ephEvent json.RawMessage) {
}
func (s *mockDataReceiver) OnInvite(ctx context.Context, userID, roomID string, inviteState []json.RawMessage) {
}
func (s *mockDataReceiver) OnLeftRoom(ctx context.Context, userID, roomID string) {}
func (s *mockDataReceiver) OnE2EEData(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) {
}
func (s *mockDataReceiver) OnTerminated(ctx context.Context, userID, deviceID string) {}
func (s *mockDataReceiver) OnExpiredToken(ctx context.Context, accessTokenHash, userID, deviceID string) {
}

func newMocks(doSyncV2 func(authHeader, since string) (*SyncResponse, int, error)) (*mockDataReceiver, *mockClient) {
	client := &mockClient{
		fn: doSyncV2,
	}
	accumulator := &mockDataReceiver{
		states:          make(map[string][]json.RawMessage),
		timelines:       make(map[string][]json.RawMessage),
		pollerIDToSince: make(map[PollerID]string),
	}
	return accumulator, client
}
