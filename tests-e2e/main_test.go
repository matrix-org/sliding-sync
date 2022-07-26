package syncv3_test

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var (
	proxyBaseURL      = "http://localhost"
	homeserverBaseURL = os.Getenv("SYNCV3_SERVER")
	userCounter       uint64
)

func TestMain(m *testing.M) {
	listenAddr := os.Getenv("SYNCV3_BINDADDR")
	if listenAddr == "" {
		fmt.Println("SYNCV3_BINDADDR must be set")
		os.Exit(1)
	}
	segments := strings.Split(listenAddr, ":")
	proxyBaseURL += ":" + segments[1]
	fmt.Println("proxy located at", proxyBaseURL)
	exitCode := m.Run()
	os.Exit(exitCode)
}

func assertEventsEqual(t *testing.T, wantList []Event, gotList []json.RawMessage) {
	t.Helper()
	if len(wantList) != len(gotList) {
		t.Errorf("got %d events, want %d", len(gotList), len(wantList))
		return
	}
	for i := 0; i < len(wantList); i++ {
		want := wantList[i]
		var got Event
		if err := json.Unmarshal(gotList[i], &got); err != nil {
			t.Errorf("failed to unmarshal event %d: %s", i, err)
		}
		if want.ID != "" && got.ID != want.ID {
			t.Errorf("event %d ID mismatch: got %v want %v", i, got.ID, want.ID)
		}
		if want.Content != nil && !reflect.DeepEqual(got.Content, want.Content) {
			t.Errorf("event %d content mismatch: got %+v want %+v", i, got.Content, want.Content)
		}
		if want.Type != "" && want.Type != got.Type {
			t.Errorf("event %d Type mismatch: got %v want %v", i, got.Type, want.Type)
		}
		if want.StateKey != nil {
			if got.StateKey == nil {
				t.Errorf("event %d StateKey mismatch: want %v got <nil>", i, *want.StateKey)
			} else if *want.StateKey != *got.StateKey {
				t.Errorf("event %d StateKey mismatch: got %v want %v", i, *got.StateKey, *want.StateKey)
			}
		}
		if want.Sender != "" && want.Sender != got.Sender {
			t.Errorf("event %d Sender mismatch: got %v want %v", i, got.Sender, want.Sender)
		}
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
	return client
}

func ptr(s string) *string {
	return &s
}
