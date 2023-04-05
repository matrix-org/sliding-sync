package caches_test

import (
	"encoding/json"
	"fmt"
	"golang.org/x/net/context"
	"reflect"
	"testing"

	"github.com/matrix-org/sliding-sync/sync3/caches"
)

type txnIDFetcher struct {
	data map[string]string
}

func (t *txnIDFetcher) TransactionIDForEvents(deviceID string, eventIDs []string) (eventIDToTxnID map[string]string) {
	eventIDToTxnID = make(map[string]string)
	for _, eventID := range eventIDs {
		txnID, ok := t.data[eventID]
		if !ok {
			continue
		}
		eventIDToTxnID[eventID] = txnID
	}
	return
}

func TestAnnotateWithTransactionIDs(t *testing.T) {
	userID := "@alice:localhost"
	testCases := []struct {
		name               string
		eventIDToTxnIDs    map[string]string
		roomIDToEvents     map[string][]string
		wantRoomIDToEvents map[string][][2]string
	}{
		{
			name: "simple 2 txn 2 rooms",
			eventIDToTxnIDs: map[string]string{
				"$foo": "txn1",
				"$bar": "txn2",
			},
			roomIDToEvents: map[string][]string{
				"!a": {"$foo"},
				"!b": {"$bar"},
			},
			wantRoomIDToEvents: map[string][][2]string{
				"!a": {{"$foo", "txn1"}},
				"!b": {{"$bar", "txn2"}},
			},
		},
		{
			name:            "no data",
			eventIDToTxnIDs: map[string]string{},
			roomIDToEvents: map[string][]string{
				"!a": {"$foo"},
				"!b": {"$bar", "$baz", "$quuz"},
			},
			wantRoomIDToEvents: map[string][][2]string{
				"!a": {{"$foo"}},
				"!b": {{"$bar"}, {"$baz"}, {"$quuz"}},
			},
		},
		{
			name: "single txn",
			eventIDToTxnIDs: map[string]string{
				"$e": "e",
			},
			roomIDToEvents: map[string][]string{
				"!a": {"$foo"},
				"!b": {"$bar"},
				"!c": {"$a", "$b", "$c", "$d", "$e"},
			},
			wantRoomIDToEvents: map[string][][2]string{
				"!a": {{"$foo"}},
				"!b": {{"$bar"}},
				"!c": {{"$a"}, {"$b"}, {"$c"}, {"$d"}, {"$e", "e"}},
			},
		},
	}
	for _, tc := range testCases {
		fetcher := &txnIDFetcher{
			data: tc.eventIDToTxnIDs,
		}
		uc := caches.NewUserCache(userID, nil, nil, fetcher)
		got := uc.AnnotateWithTransactionIDs(context.Background(), "DEVICE", convertIDToEventStub(tc.roomIDToEvents))
		want := convertIDTxnToEventStub(tc.wantRoomIDToEvents)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s : got %v want %v", tc.name, js(got), js(want))
		}
	}
}

func js(in interface{}) string {
	b, _ := json.Marshal(in)
	return string(b)
}

func convertIDToEventStub(roomToEventIDs map[string][]string) map[string][]json.RawMessage {
	result := make(map[string][]json.RawMessage)
	for roomID, eventIDs := range roomToEventIDs {
		events := make([]json.RawMessage, len(eventIDs))
		for i := range eventIDs {
			events[i] = json.RawMessage(fmt.Sprintf(`{"event_id":"%s","type":"x"}`, eventIDs[i]))
		}
		result[roomID] = events
	}
	return result
}

func convertIDTxnToEventStub(roomToEventIDs map[string][][2]string) map[string][]json.RawMessage {
	result := make(map[string][]json.RawMessage)
	for roomID, eventIDs := range roomToEventIDs {
		events := make([]json.RawMessage, len(eventIDs))
		for i := range eventIDs {
			if eventIDs[i][1] == "" {
				events[i] = json.RawMessage(fmt.Sprintf(`{"event_id":"%s","type":"x"}`, eventIDs[i][0]))
			} else {
				events[i] = json.RawMessage(fmt.Sprintf(`{"event_id":"%s","type":"x","unsigned":{"transaction_id":"%s"}}`, eventIDs[i][0], eventIDs[i][1]))
			}
		}
		result[roomID] = events
	}
	return result
}
