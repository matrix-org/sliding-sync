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

func generateEventID() string {
	eventIDMu.Lock()
	defer eventIDMu.Unlock()
	eventIDCounter++
	return fmt.Sprintf("$event_%d", eventIDCounter)
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
		EventID:  generateEventID(),
	}
	j, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("failed to make event JSON: %s", err)
	}
	return j
}

func NewEvent(t *testing.T, evType, sender string, content interface{}) json.RawMessage {
	t.Helper()
	e := struct {
		Type    string      `json:"type"`
		Sender  string      `json:"sender"`
		Content interface{} `json:"content"`
		EventID string      `json:"event_id"`
	}{
		Type:    evType,
		Sender:  sender,
		Content: content,
		EventID: generateEventID(),
	}
	j, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("failed to make event JSON: %s", err)
	}
	return j
}
