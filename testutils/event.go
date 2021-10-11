package testutils

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

var (
	eventIDCounter = 0
	eventIDMu      sync.Mutex
)

func generateEventID(t *testing.T) string {
	eventIDMu.Lock()
	defer eventIDMu.Unlock()
	eventIDCounter++
	// we need to mux in the test name bleurgh because when run using `go test ./...` each
	// package's tests run in parallel but with a shared database when run on Github Actions
	return fmt.Sprintf("$event_%d_%s", eventIDCounter, t.Name())
}

func NewStateEvent(t *testing.T, evType, stateKey, sender string, content interface{}) json.RawMessage {
	t.Helper()
	e := struct {
		Type     string      `json:"type"`
		StateKey string      `json:"state_key"`
		Sender   string      `json:"sender"`
		Content  interface{} `json:"content"`
		EventID  string      `json:"event_id"`
	}{
		Type:     evType,
		StateKey: stateKey,
		Sender:   sender,
		Content:  content,
		EventID:  generateEventID(t),
	}
	j, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("failed to make event JSON: %s", err)
	}
	return j
}

func NewEvent(t *testing.T, evType, sender string, content interface{}, originServerTs int64) json.RawMessage {
	t.Helper()
	e := struct {
		Type    string      `json:"type"`
		Sender  string      `json:"sender"`
		Content interface{} `json:"content"`
		EventID string      `json:"event_id"`
		TS      int64       `json:"origin_server_ts"`
	}{
		Type:    evType,
		Sender:  sender,
		Content: content,
		EventID: generateEventID(t),
		TS:      originServerTs,
	}
	j, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("failed to make event JSON: %s", err)
	}
	return j
}
