package extensions

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/tidwall/gjson"
)

// Test that aggregation works, which is hard to assert in integration tests
func TestLiveTypingAggregation(t *testing.T) {
	boolTrue := true
	ext := &TypingRequest{
		Core: Core{
			Enabled: &boolTrue,
			Lists:   []string{"*"},
			Rooms:   []string{"*"},
		},
	}
	var res Response
	extCtx := Context{
		AllSubscribedRooms: []string{roomA, roomB, roomC},
	}
	typingA1 := &caches.TypingUpdate{
		RoomUpdate: &dummyRoomUpdate{
			roomID: roomA,
			globalMetadata: &internal.RoomMetadata{
				RoomID:      roomA,
				TypingEvent: json.RawMessage(`{"type":"m.typing","content":{"user_ids":["@alice:localhost"]}}`),
			},
		},
	}
	typingB1 := &caches.TypingUpdate{
		RoomUpdate: &dummyRoomUpdate{
			roomID: roomB,
			globalMetadata: &internal.RoomMetadata{
				RoomID:      roomB,
				TypingEvent: json.RawMessage(`{"type":"m.typing","content":{"user_ids":["@bob:localhost"]}}`),
			},
		},
	}
	typingA2 := &caches.TypingUpdate{ // this should replace typingA1 as it clobbers on roomID
		RoomUpdate: &dummyRoomUpdate{
			roomID: roomA,
			globalMetadata: &internal.RoomMetadata{
				RoomID:      roomA,
				TypingEvent: json.RawMessage(`{"type":"m.typing","content":{"user_ids":["@charlie:localhost"]}}`),
			},
		},
	}
	ext.AppendLive(ctx, &res, extCtx, typingA1)
	ext.AppendLive(ctx, &res, extCtx, typingB1)
	ext.AppendLive(ctx, &res, extCtx, typingA2)
	if res.Typing == nil {
		t.Fatalf("typing response is empty")
	}
	want := map[string]json.RawMessage{
		roomA: typingA2.GlobalRoomMetadata().TypingEvent,
		roomB: typingB1.GlobalRoomMetadata().TypingEvent,
	}
	if !reflect.DeepEqual(res.Typing.Rooms, want) {
		t.Fatalf("got  %+v\nwant %+v", res.Typing.Rooms, want)
	}

	// now add a message: we should include typing members at this time.
	eventC1 := &caches.RoomEventUpdate{
		RoomUpdate: &dummyRoomUpdate{
			roomID: roomC,
			globalMetadata: &internal.RoomMetadata{
				RoomID:      roomC,
				TypingEvent: json.RawMessage(`{"type":"m.typing","content":{"user_ids":["@doris:localhost"]}}`),
			},
		},
		EventData: &caches.EventData{
			RoomID:    roomC,
			EventType: "m.room.message",
			Content:   gjson.Parse(`{"body":"hello world"}`),
			Timestamp: 123456,
		},
	}
	extCtx.RoomIDToTimeline = map[string][]string{
		roomC: {"$c"},
	}
	ext.AppendLive(ctx, &res, extCtx, eventC1)
	want[roomC] = eventC1.GlobalRoomMetadata().TypingEvent
	if !reflect.DeepEqual(res.Typing.Rooms, want) {
		t.Fatalf("got  %s\nwant %s", res.Typing.Rooms, want)
	}
}
