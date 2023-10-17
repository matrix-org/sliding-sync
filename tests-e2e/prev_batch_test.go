package syncv3_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"testing"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/complement/client"
	"github.com/matrix-org/complement/must"
	"github.com/matrix-org/sliding-sync/sync3"
)

func TestPrevBatch(t *testing.T) {
	cli := registerNewUser(t)

	// create room
	roomID := cli.MustCreateRoom(t, map[string]interface{}{})
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
		ev.ID = cli.SendEventSynced(t, roomID, b.Event{
			Type:    ev.Type,
			Content: ev.Content,
		})
		timeline = append(timeline, ev)
	}

	// hit proxy
	res := cli.SlidingSync(t, sync3.Request{
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
	msgRes := cli.MustDo(t, "GET", []string{"_matrix", "client", "v3", "rooms", roomID, "messages"}, client.WithQueries(url.Values{
		"dir":   []string{"b"},
		"from":  []string{room.PrevBatch},
		"limit": []string{"10"},
	}))
	body, err := io.ReadAll(msgRes.Body)
	msgRes.Body.Close()
	must.NotError(t, "failed to read response body", err)

	type MessagesBatch struct {
		Chunk []json.RawMessage `json:"chunk"`
		Start string            `json:"start"`
		End   string            `json:"end"`
	}
	var msgBody MessagesBatch
	if err := json.Unmarshal(body, &msgBody); err != nil {
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
