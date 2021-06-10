package sync2

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Check that a call to Poll starts polling and accumulating, and terminates on 401s.
func TestPollerPollFromNothing(t *testing.T) {
	nextSince := "next"
	deviceID := "FOOBAR"
	roomID := "!foo:bar"
	roomState := []json.RawMessage{
		json.RawMessage(`{"event":1}`),
		json.RawMessage(`{"event":2}`),
		json.RawMessage(`{"event":3}`),
	}
	hasPolledSuccessfully := false
	accumulator, client, sessions := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
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
	poller := newPoller("Authorization: hello world", deviceID, client, accumulator, sessions)
	go func() {
		defer wg.Done()
		poller.Poll("", func() {
			hasPolledSuccessfully = true
		})
	}()
	wg.Wait()
	if !hasPolledSuccessfully {
		t.Errorf("failed to poll successfully")
	}
	if len(accumulator.states[roomID]) != len(roomState) {
		t.Errorf("did not accumulate initial state for room, got %d events want %d", len(accumulator.states[roomID]), len(roomState))
	}
	if sessions.deviceIDToSince[deviceID] != nextSince {
		t.Errorf("did not persist latest since token, got %s want %s", sessions.deviceIDToSince[deviceID], nextSince)
	}
}

// Check that a call to Poll starts polling with an existing since token and accumulates timeline entries
func TestPollerPollFromExisting(t *testing.T) {
	deviceID := "FOOBAR"
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
	hasPolledSuccessfully := false
	accumulator, client, sessions := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
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
	poller := newPoller("Authorization: hello world", deviceID, client, accumulator, sessions)
	go func() {
		defer wg.Done()
		poller.Poll(since, func() {
			hasPolledSuccessfully = true
		})
	}()
	wg.Wait()
	if !hasPolledSuccessfully {
		t.Errorf("failed to poll successfully")
	}
	if len(accumulator.timelines[roomID]) != 10 {
		t.Errorf("did not accumulate timelines for room, got %d events want %d", len(accumulator.timelines[roomID]), 10)
	}
	wantSince := fmt.Sprintf("%d", len(roomTimelineResponses))
	if sessions.deviceIDToSince[deviceID] != wantSince {
		t.Errorf("did not persist latest since token, got %s want %s", sessions.deviceIDToSince[deviceID], wantSince)
	}
}

// Tests that the poller backs off in 2,4,8,etc second increments to a variety of errors
func TestPollerBackoff(t *testing.T) {
	deviceID := "FOOBAR"
	hasPolledSuccessfully := false
	errorResponses := []struct {
		code    int
		backoff time.Duration
		err     error
	}{
		{
			code:    0,
			err:     fmt.Errorf("network error"),
			backoff: 2 * time.Second,
		},
		{
			code:    500,
			err:     fmt.Errorf("internal server error"),
			backoff: 4 * time.Second,
		},
		{
			code:    502,
			err:     fmt.Errorf("bad gateway error"),
			backoff: 8 * time.Second,
		},
		{
			code:    404,
			err:     fmt.Errorf("not found"),
			backoff: 16 * time.Second,
		},
	}
	errorResponsesIndex := 0
	var wantBackoffDuration time.Duration
	accumulator, client, sessions := newMocks(func(authHeader, since string) (*SyncResponse, int, error) {
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
	}
	var wg sync.WaitGroup
	wg.Add(1)
	poller := newPoller("Authorization: hello world", deviceID, client, accumulator, sessions)
	go func() {
		defer wg.Done()
		poller.Poll("some_since_value", func() {
			hasPolledSuccessfully = true
		})
	}()
	wg.Wait()
	if hasPolledSuccessfully {
		t.Errorf("Incorrectly polled successfully")
	}
	if errorResponsesIndex != len(errorResponses) {
		t.Errorf("did not call DoSyncV2 enough, got %d times, want %d", errorResponsesIndex+1, len(errorResponses))
	}
}

type mockClient struct {
	fn func(authHeader, since string) (*SyncResponse, int, error)
}

func (c *mockClient) DoSyncV2(authHeader, since string) (*SyncResponse, int, error) {
	return c.fn(authHeader, since)
}

type mockAccumulator struct {
	states    map[string][]json.RawMessage
	timelines map[string][]json.RawMessage
}

func (a *mockAccumulator) Accumulate(roomID string, timeline []json.RawMessage) error {
	a.timelines[roomID] = append(a.timelines[roomID], timeline...)
	return nil
}
func (a *mockAccumulator) Initialise(roomID string, state []json.RawMessage) error {
	a.states[roomID] = state
	return nil
}
func (a *mockAccumulator) SetTyping(roomID string, userIDs []string) error {
	return nil
}

type mockSessions struct {
	deviceIDToSince map[string]string
}

func (s *mockSessions) UpdateDeviceSince(deviceID, since string) error {
	s.deviceIDToSince[deviceID] = since
	return nil
}

func newMocks(doSyncV2 func(authHeader, since string) (*SyncResponse, int, error)) (*mockAccumulator, *mockClient, *mockSessions) {
	client := &mockClient{
		fn: doSyncV2,
	}
	accumulator := &mockAccumulator{
		states:    make(map[string][]json.RawMessage),
		timelines: make(map[string][]json.RawMessage),
	}
	sessions := &mockSessions{
		deviceIDToSince: make(map[string]string),
	}
	return accumulator, client, sessions
}
