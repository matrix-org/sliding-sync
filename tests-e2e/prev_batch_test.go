package syncv3_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync3"
)

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
		if got.ID != want.ID {
			t.Errorf("event %d ID mismatch: got %v want %v", i, got.ID, want.ID)
		}
		if !reflect.DeepEqual(got.Content, want.Content) {
			t.Errorf("event %d content mismatch: got %+v want %+v", i, got.Content, want.Content)
		}
	}
}

func TestPrevBatch(t *testing.T) {
	// create user
	httpClient := NewLoggedClient(t, "localhost", nil)
	client := &CSAPI{
		Client:           httpClient,
		BaseURL:          homeserverBaseURL,
		SyncUntilTimeout: 3 * time.Second,
	}
	client.UserID, client.AccessToken, client.DeviceID = client.RegisterUser(t, "alice", "password")

	// create room
	roomID := client.CreateRoom(t, map[string]interface{}{})
	var timeline []Event
	// send messages
	for i := 0; i < 30; i++ {
		ev := Event{
			Type: "m.room.message",
			Content: map[string]interface{}{
				"body":    fmt.Sprintf("Message %d", i),
				"msgtype": "m.text",
			},
		}
		ev.ID = client.SendEventSynced(t, roomID, ev)
		timeline = append(timeline, ev)
	}

	// hit proxy
	res := client.SlidingSync(t, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{[2]int64{0, 10}},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
			},
		},
	})
	room, ok := res.Rooms[roomID]
	if !ok {
		t.Fatalf("failed to find room %s in response %+v", roomID, res)
	}
	if room.PrevBatch == "" {
		t.Fatalf("room %s has no prev_batch token", roomID)
	}
	assertEventsEqual(t, []Event{timeline[len(timeline)-1]}, room.Timeline)

	// hit /messages with prev_batch token
	msgRes := client.MustDoFunc(t, "GET", []string{"_matrix", "client", "v3", "rooms", roomID, "messages"}, WithQueries(url.Values{
		"dir":   []string{"b"},
		"from":  []string{room.PrevBatch},
		"limit": []string{"10"},
	}))
	var msgBody MessagesBatch
	if err := json.Unmarshal(ParseJSON(t, msgRes), &msgBody); err != nil {
		t.Fatalf("failed to unmarshal /messages response: %v", err)
	}
	// reverse it
	history := make([]json.RawMessage, len(msgBody.Chunk))
	for i := 0; i < len(history); i++ {
		history[i] = msgBody.Chunk[len(history)-1-i]
	}

	// assert order is correct
	assertEventsEqual(t, timeline[len(timeline)-11:len(timeline)-1], history)
}
