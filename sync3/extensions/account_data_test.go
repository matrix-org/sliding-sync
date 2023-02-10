package extensions

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

// Test that aggregation works, which is hard to assert in integration tests
func TestLiveAccountDataAggregation(t *testing.T) {
	boolTrue := true
	ext := &AccountDataRequest{
		Enableable: Enableable{
			Enabled: &boolTrue,
		},
	}
	var res Response
	var extCtx Context
	room1 := &caches.RoomAccountDataUpdate{
		RoomUpdate: &dummyRoomUpdate{
			roomID: roomA,
			globalMetadata: &internal.RoomMetadata{
				RoomID: roomA,
			},
		},
		AccountData: []state.AccountData{
			{
				Data: []byte(`{"foo":"bar"}`),
			},
			{
				Data: []byte(`{"foo2":"bar2"}`),
			},
		},
	}
	room2 := &caches.RoomAccountDataUpdate{
		RoomUpdate: &dummyRoomUpdate{
			roomID: roomA,
			globalMetadata: &internal.RoomMetadata{
				RoomID: roomA,
			},
		},
		AccountData: []state.AccountData{
			{
				Data: []byte(`{"foo3":"bar3"}`),
			},
			{
				Data: []byte(`{"foo4":"bar4"}`),
			},
		},
	}
	global1 := &caches.AccountDataUpdate{
		AccountData: []state.AccountData{
			{
				Data: []byte(`{"global":"bar"}`),
			},
			{
				Data: []byte(`{"global2":"bar2"}`),
			},
		},
	}
	global2 := &caches.AccountDataUpdate{
		AccountData: []state.AccountData{
			{
				Data: []byte(`{"global3":"bar3"}`),
			},
			{
				Data: []byte(`{"global4":"bar4"}`),
			},
		},
	}
	ext.AppendLive(ctx, &res, extCtx, room1)
	wantRoomAccountData := map[string][]json.RawMessage{
		roomA: []json.RawMessage{
			room1.AccountData[0].Data, room1.AccountData[1].Data,
		},
	}
	if !reflect.DeepEqual(res.AccountData.Rooms, wantRoomAccountData) {
		t.Fatalf("got  %+v\nwant %+v", res.AccountData.Rooms, wantRoomAccountData)
	}
	ext.AppendLive(ctx, &res, extCtx, room2)
	ext.AppendLive(ctx, &res, extCtx, global1)
	ext.AppendLive(ctx, &res, extCtx, global2)
	if res.AccountData == nil {
		t.Fatalf("account_data response is empty")
	}
	wantRoomAccountData = map[string][]json.RawMessage{
		roomA: []json.RawMessage{
			room1.AccountData[0].Data, room1.AccountData[1].Data,
			room2.AccountData[0].Data, room2.AccountData[1].Data,
		},
	}
	if !reflect.DeepEqual(res.AccountData.Rooms, wantRoomAccountData) {
		t.Fatalf("got  %+v\nwant %+v", res.AccountData.Rooms, wantRoomAccountData)
	}

	wantGlobalAccountData := []json.RawMessage{
		global1.AccountData[0].Data, global1.AccountData[1].Data,
		global2.AccountData[0].Data, global2.AccountData[1].Data,
	}
	if !reflect.DeepEqual(res.AccountData.Global, wantGlobalAccountData) {
		t.Fatalf("got  %+v\nwant %+v", res.AccountData.Global, wantGlobalAccountData)
	}
}
