package syncv3_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	"github.com/matrix-org/sliding-sync/sync3"
)

func TestPrevBatch(t *testing.T) {
	client := registerNewUser(t)

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
		Lists: map[string]sync3.RequestList{
			"a": {
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
