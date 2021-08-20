package testutils

import (
	"encoding/json"
	"fmt"
	"testing"
)

var eventIDCounter = 0

func NewStateEvent(t *testing.T, evType, stateKey, sender string, content interface{}) json.RawMessage {
	t.Helper()
	eventIDCounter++
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
		EventID:  fmt.Sprintf("$event_%d", eventIDCounter),
	}
	j, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("failed to make event JSON: %s", err)
	}
	return j
}
