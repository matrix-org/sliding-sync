package syncv3_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

var (
	proxyBaseURL      = os.Getenv("SYNCV3_ADDR")
	homeserverBaseURL = os.Getenv("SYNCV3_SERVER")
	userCounter       uint64
)

func TestMain(m *testing.M) {
	if proxyBaseURL == "" {
		fmt.Println("SYNCV3_ADDR must be set e.g 'http://localhost:8008'")
		os.Exit(1)
	}
	fmt.Println("proxy located at", proxyBaseURL)
	exitCode := m.Run()
	os.Exit(exitCode)
}

func WithPos(pos string) RequestOpt {
	return WithQueries(url.Values{
		"pos":     []string{pos},
		"timeout": []string{"500"}, // 0.5s
	})
}

func assertEventsEqual(t *testing.T, wantList []Event, gotList []json.RawMessage) {
	t.Helper()
	err := eventsEqual(wantList, gotList)
	if err != nil {
		t.Errorf(err.Error())
	}
}

func eventsEqual(wantList []Event, gotList []json.RawMessage) error {
	if len(wantList) != len(gotList) {
		return fmt.Errorf("got %d events, want %d", len(gotList), len(wantList))
	}
	for i := 0; i < len(wantList); i++ {
		want := wantList[i]
		var got Event
		if err := json.Unmarshal(gotList[i], &got); err != nil {
			return fmt.Errorf("failed to unmarshal event %d: %s", i, err)
		}
		if want.ID != "" && got.ID != want.ID {
			return fmt.Errorf("event %d ID mismatch: got %v want %v", i, got.ID, want.ID)
		}
		if want.Content != nil && !reflect.DeepEqual(got.Content, want.Content) {
			return fmt.Errorf("event %d content mismatch: got %+v want %+v", i, got.Content, want.Content)
		}
		if want.Type != "" && want.Type != got.Type {
			return fmt.Errorf("event %d Type mismatch: got %v want %v", i, got.Type, want.Type)
		}
		if want.StateKey != nil {
			if got.StateKey == nil {
				return fmt.Errorf("event %d StateKey mismatch: want %v got <nil>", i, *want.StateKey)
			} else if *want.StateKey != *got.StateKey {
				return fmt.Errorf("event %d StateKey mismatch: got %v want %v", i, *got.StateKey, *want.StateKey)
			}
		}
		if want.Sender != "" && want.Sender != got.Sender {
			return fmt.Errorf("event %d Sender mismatch: got %v want %v", i, got.Sender, want.Sender)
		}
	}
	return nil
}

// MatchRoomTimelineMostRecent builds a matcher which checks that the last `n` elements
// of `events` are the same as the last n elements of the room timeline. If either list
// contains fewer than `n` events, the match fails.
// Events are tested for equality using `eventsEqual`.
func MatchRoomTimelineMostRecent(n int, events []Event) m.RoomMatcher {
	return func(r sync3.Room) error {
		if len(events) < n {
			return fmt.Errorf("list of wanted events has %d events, expected at least %d", len(events), n)
		}
		wantList := events[len(events)-n:]
		if len(r.Timeline) < n {
			return fmt.Errorf("timeline has %d events, expected at least %d", len(r.Timeline), n)
		}

		gotList := r.Timeline[len(r.Timeline)-n:]
		return eventsEqual(wantList, gotList)
	}
}

func MatchRoomTimeline(events []Event) m.RoomMatcher {
	return func(r sync3.Room) error {
		return eventsEqual(events, r.Timeline)
	}
}

func MatchRoomTimelineContains(event Event) m.RoomMatcher {
	return func(r sync3.Room) error {
		var err error
		for _, got := range r.Timeline {
			if err = eventsEqual([]Event{event}, []json.RawMessage{got}); err == nil {
				return nil
			}
		}
		return err
	}
}

func MatchRoomRequiredState(events []Event) m.RoomMatcher {
	return func(r sync3.Room) error {
		if len(r.RequiredState) != len(events) {
			return fmt.Errorf("required state length mismatch, got %d want %d", len(r.RequiredState), len(events))
		}
		// allow any ordering for required state
		for _, want := range events {
			found := false
			for _, got := range r.RequiredState {
				if err := eventsEqual([]Event{want}, []json.RawMessage{got}); err == nil {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("required state want event %+v but did not find exact match", want)
			}
		}
		return nil
	}
}

func MatchRoomInviteState(events []Event, partial bool) m.RoomMatcher {
	return func(r sync3.Room) error {
		if !partial && len(r.InviteState) != len(events) {
			return fmt.Errorf("MatchRoomInviteState: length mismatch, got %d want %d", len(r.InviteState), len(events))
		}
		// allow any ordering for state
		for _, want := range events {
			found := false
			for _, got := range r.InviteState {
				if err := eventsEqual([]Event{want}, []json.RawMessage{got}); err == nil {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("MatchRoomInviteState: want event %+v but it does not exist", want)
			}
		}
		return nil
	}
}

func registerNewUser(t *testing.T) *CSAPI {
	// create user
	httpClient := NewLoggedClient(t, "localhost", nil)
	client := &CSAPI{
		Client:           httpClient,
		BaseURL:          homeserverBaseURL,
		SyncUntilTimeout: 3 * time.Second,
	}
	localpart := fmt.Sprintf("user-%d-%d", time.Now().Unix(), atomic.AddUint64(&userCounter, 1))

	client.UserID, client.AccessToken, client.DeviceID = client.RegisterUser(t, localpart, "password")
	client.Localpart = strings.Split(client.UserID, ":")[0][1:]
	return client
}

func ptr(s string) *string {
	return &s
}
