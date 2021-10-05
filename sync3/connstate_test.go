package sync3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

type connStateStoreMock struct {
	roomIDToRoom        map[string]*SortableRoom
	userIDToJoinedRooms map[string][]string
	userIDToPosition    map[string]int64
}

func (s *connStateStoreMock) LoadRoom(roomID string) *SortableRoom {
	return s.roomIDToRoom[roomID]
}
func (s *connStateStoreMock) Load(userID string) (joinedRoomIDs []string, initialLoadPosition int64, err error) {
	joinedRoomIDs = s.userIDToJoinedRooms[userID]
	initialLoadPosition = s.userIDToPosition[userID]
	if initialLoadPosition == 0 {
		initialLoadPosition = 1 // so we don't continually load the same rooms
	}
	return
}
func (s *connStateStoreMock) PushNewEvent(cs *ConnState, ed *EventData) {
	room := s.roomIDToRoom[ed.roomID]
	room.LastEventJSON = ed.event
	room.LastMessageTimestamp = ed.timestamp
	if ed.eventType == "m.room.name" {
		room.Name = ed.content.Get("name").Str
	}
	s.roomIDToRoom[ed.roomID] = room
	cs.PushNewEvent(ed)
}

// Sync an account with 3 rooms and check that we can grab all rooms and they are sorted correctly initially. Checks
// that basic UPDATE and DELETE/INSERT works when tracking all rooms.
func TestConnStateInitial(t *testing.T) {
	connID := ConnID{
		SessionID: "s",
		DeviceID:  "d",
	}
	userID := "@alice:localhost"
	roomA := "!a:localhost"
	roomB := "!b:localhost"
	roomC := "!c:localhost"
	timestampNow := int64(1632131678061)
	// initial sort order B, C, A
	csm := &connStateStoreMock{
		userIDToJoinedRooms: map[string][]string{
			userID: {roomA, roomB, roomC},
		},
		roomIDToRoom: map[string]*SortableRoom{
			roomA: {
				RoomID:               roomA,
				Name:                 "Room A",
				LastMessageTimestamp: timestampNow - 8000,
			},
			roomB: {
				RoomID:               roomB,
				Name:                 "Room B",
				LastMessageTimestamp: timestampNow,
			},
			roomC: {
				RoomID:               roomC,
				Name:                 "Room C",
				LastMessageTimestamp: timestampNow - 4000,
			},
		},
	}
	cs := NewConnState(userID, csm)
	if userID != cs.UserID() {
		t.Fatalf("UserID returned wrong value, got %v want %v", cs.UserID(), userID)
	}
	res, err := cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 9},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, false, res, &Response{
		Count: 3,
		Ops: []ResponseOp{
			&ResponseOpRange{
				Operation: "SYNC",
				Range:     []int64{0, 9},
				Rooms: []Room{
					{
						RoomID: roomB,
						Name:   "Room B",
					},
					{
						RoomID: roomC,
						Name:   "Room C",
					},
					{
						RoomID: roomA,
						Name:   "Room A",
					},
				},
			},
		},
	})

	// bump A to the top
	csm.PushNewEvent(cs, &EventData{
		event:     json.RawMessage(`{}`),
		roomID:    roomA,
		eventType: "unimportant",
		timestamp: timestampNow + 1000,
	})

	// request again for the diff
	res, err = cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 9},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &Response{
		Count: 3,
		Ops: []ResponseOp{
			&ResponseOpSingle{
				Operation: "DELETE",
				Index:     intPtr(2),
			},
			&ResponseOpSingle{
				Operation: "INSERT",
				Index:     intPtr(0),
				Room: &Room{
					RoomID: roomA,
				},
			},
		},
	})

	// another message should just update
	csm.PushNewEvent(cs, &EventData{
		event:     json.RawMessage(`{}`),
		roomID:    roomA,
		eventType: "still unimportant",
		timestamp: timestampNow + 2000,
	})
	res, err = cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 9},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &Response{
		Count: 3,
		Ops: []ResponseOp{
			&ResponseOpSingle{
				Operation: "UPDATE",
				Index:     intPtr(0),
				Room: &Room{
					RoomID: roomA,
				},
			},
		},
	})
}

func TestConnStateMultipleRanges(t *testing.T) {
	connID := ConnID{
		SessionID: "s",
		DeviceID:  "d",
	}
	userID := "@alice:localhost"
	timestampNow := int64(1632131678061)
	var rooms []*SortableRoom
	var roomIDs []string
	roomIDToRoom := make(map[string]*SortableRoom)
	for i := 0; i < 10; i++ {
		roomID := fmt.Sprintf("!%d:localhost", i)
		room := &SortableRoom{
			RoomID: roomID,
			Name:   fmt.Sprintf("Room %d", i),
			// room 1 is most recent, 10 is least recent
			LastMessageTimestamp: timestampNow - int64(i*1000),
			LastEventJSON:        []byte(`{}`),
		}
		rooms = append(rooms, room)
		roomIDs = append(roomIDs, roomID)
		roomIDToRoom[roomID] = room
	}

	// initial sort order B, C, A
	csm := &connStateStoreMock{
		userIDToJoinedRooms: map[string][]string{
			userID: roomIDs,
		},
		roomIDToRoom: roomIDToRoom,
	}
	cs := NewConnState(userID, csm)

	// request first page
	res, err := cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 2},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &Response{
		Count: int64(len(rooms)),
		Ops: []ResponseOp{
			&ResponseOpRange{
				Operation: "SYNC",
				Range:     []int64{0, 2},
				Rooms: []Room{
					{
						RoomID: roomIDs[0],
					},
					{
						RoomID: roomIDs[1],
					},
					{
						RoomID: roomIDs[2],
					},
				},
			},
		},
	})
	// add on a different non-overlapping range
	res, err = cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 2}, {4, 6},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &Response{
		Count: int64(len(rooms)),
		Ops: []ResponseOp{
			&ResponseOpRange{
				Operation: "SYNC",
				Range:     []int64{4, 6},
				Rooms: []Room{
					{
						RoomID: roomIDs[4],
					},
					{
						RoomID: roomIDs[5],
					},
					{
						RoomID: roomIDs[6],
					},
				},
			},
		},
	})

	// pull room 8 to position 0 should result in DELETE[6] and INSERT[0]
	// 0,1,2,3,4,5,6,7,8,9
	// `----`  `----`
	// `    `  `    `
	// 8,0,1,2,3,4,5,6,7,9
	//
	csm.PushNewEvent(cs, &EventData{
		event:     json.RawMessage(`{}`),
		roomID:    roomIDs[8],
		eventType: "unimportant",
		timestamp: timestampNow + 2000,
	})

	res, err = cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 2}, {4, 6},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &Response{
		Count: int64(len(rooms)),
		Ops: []ResponseOp{
			&ResponseOpSingle{
				Operation: "DELETE",
				Index:     intPtr(6),
			},
			&ResponseOpSingle{
				Operation: "INSERT",
				Index:     intPtr(0),
				Room: &Room{
					RoomID: roomIDs[8],
				},
			},
		},
	})

	// pull room 9 to position 3 should result in DELETE[6] and INSERT[4] with room 2
	// 0,1,2,3,4,5,6,7,8,9 index
	// 8,0,1,2,3,4,5,6,7,9 room
	// `----`  `----`
	// `    `  `    `
	// 8,0,1,9,2,3,4,5,6,7 room
	middleTimestamp := int64((roomIDToRoom[roomIDs[1]].LastMessageTimestamp + roomIDToRoom[roomIDs[2]].LastMessageTimestamp) / 2)
	csm.PushNewEvent(cs, &EventData{
		event:     json.RawMessage(`{}`),
		roomID:    roomIDs[9],
		eventType: "unimportant",
		timestamp: middleTimestamp,
	})
	res, err = cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 2}, {4, 6},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &Response{
		Count: int64(len(rooms)),
		Ops: []ResponseOp{
			&ResponseOpSingle{
				Operation: "DELETE",
				Index:     intPtr(6),
			},
			&ResponseOpSingle{
				Operation: "INSERT",
				Index:     intPtr(4),
				Room: &Room{
					RoomID: roomIDs[2],
				},
			},
		},
	})
}

func TestConnStateRoomSubscriptions(t *testing.T) {
	connID := ConnID{
		SessionID: "s",
		DeviceID:  "d",
	}
	userID := "@alice:localhost"
	roomA := "!a:localhost"
	roomB := "!b:localhost"
	roomC := "!c:localhost"
	roomD := "!d:localhost"
	roomIDs := []string{roomA, roomB, roomC, roomD}
	timestampNow := int64(1632131678061)
	// initial sort order A,B,C,D
	csm := &connStateStoreMock{
		userIDToJoinedRooms: map[string][]string{
			userID: roomIDs,
		},
		roomIDToRoom: map[string]*SortableRoom{
			roomA: {
				RoomID:               roomA,
				Name:                 "Room A",
				LastMessageTimestamp: timestampNow,
				LastEventJSON:        json.RawMessage(fmt.Sprintf(`{"origin_server_ts":%d, "type":"m.room.message", "a":"yep"}`, timestampNow)),
			},
			roomB: {
				RoomID:               roomB,
				Name:                 "Room B",
				LastMessageTimestamp: timestampNow - 2000,
				LastEventJSON:        json.RawMessage(fmt.Sprintf(`{"origin_server_ts":%d, "type":"m.room.message", "b":"yep"}`, timestampNow-2000)),
			},
			roomC: {
				RoomID:               roomC,
				Name:                 "Room C",
				LastMessageTimestamp: timestampNow - 4000,
				LastEventJSON:        json.RawMessage(fmt.Sprintf(`{"origin_server_ts":%d, "type":"m.room.message", "c":"yep"}`, timestampNow-4000)),
			},
			roomD: {
				RoomID:               roomD,
				Name:                 "Room D",
				LastMessageTimestamp: timestampNow - 6000,
				LastEventJSON:        json.RawMessage(fmt.Sprintf(`{"origin_server_ts":%d, "type":"m.room.message", "d":"yep"}`, timestampNow-6000)),
			},
		},
	}
	cs := NewConnState(userID, csm)
	// subscribe to room D
	res, err := cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		RoomSubscriptions: map[string]RoomSubscription{
			roomD: {
				TimelineLimit: 20,
			},
		},
		Rooms: SliceRanges([][2]int64{
			{0, 1},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, false, res, &Response{
		Count: int64(len(roomIDs)),
		RoomSubscriptions: map[string]Room{
			roomD: {
				RoomID: roomD,
				Name:   "Room D",
				Timeline: []json.RawMessage{
					csm.roomIDToRoom[roomD].LastEventJSON,
				},
			},
		},
		Ops: []ResponseOp{
			&ResponseOpRange{
				Operation: "SYNC",
				Range:     []int64{0, 1},
				Rooms: []Room{
					{
						RoomID: roomIDs[0],
						Name:   csm.roomIDToRoom[roomIDs[0]].Name,
						Timeline: []json.RawMessage{
							csm.roomIDToRoom[roomIDs[0]].LastEventJSON,
						},
					},
					{
						RoomID: roomIDs[1],
						Name:   csm.roomIDToRoom[roomIDs[1]].Name,
						Timeline: []json.RawMessage{
							csm.roomIDToRoom[roomIDs[1]].LastEventJSON,
						},
					},
				},
			},
		},
	})
	// room D gets a new event
	csm.PushNewEvent(cs, &EventData{
		event:     json.RawMessage(`{}`),
		roomID:    roomD,
		eventType: "unimportant",
		timestamp: timestampNow + 2000,
	})
	// we should get this message even though it's not in the range because we are subscribed to this room.
	res, err = cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 1},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, false, res, &Response{
		Count: int64(len(roomIDs)),
		RoomSubscriptions: map[string]Room{
			roomD: {
				RoomID: roomD,
				Timeline: []json.RawMessage{
					json.RawMessage(`{}`),
				},
			},
		},
		// TODO: index markers as this new event should bump D into the tracked range
	})

	// now swap to room C
	res, err = cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		RoomSubscriptions: map[string]RoomSubscription{
			roomC: {
				TimelineLimit: 20,
			},
		},
		UnsubscribeRooms: []string{roomD},
		Rooms: SliceRanges([][2]int64{
			{0, 1},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, false, res, &Response{
		Count: int64(len(roomIDs)),
		RoomSubscriptions: map[string]Room{
			roomC: {
				RoomID: roomC,
				Name:   "Room C",
				Timeline: []json.RawMessage{
					csm.roomIDToRoom[roomC].LastEventJSON,
				},
			},
		},
	})
}

func checkResponse(t *testing.T, checkRoomIDsOnly bool, got, want *Response) {
	t.Helper()
	if want.Count > 0 {
		if got.Count != want.Count {
			t.Errorf("response Count: got %d want %d", got.Count, want.Count)
		}
	}
	if len(want.Ops) > 0 {
		t.Logf("got %v", serialise(t, got))
		t.Logf("want %v", serialise(t, want))
		defer func() {
			t.Helper()
			if !t.Failed() {
				t.Logf("OK!")
			}
		}()
		if len(got.Ops) != len(want.Ops) {
			t.Fatalf("got %d ops, want %d", len(got.Ops), len(want.Ops))
		}
		for i, wantOpVal := range want.Ops {
			gotOp := got.Ops[i]
			if gotOp.Op() != wantOpVal.Op() {
				t.Errorf("operation i=%d got '%s' want '%s'", i, gotOp.Op(), wantOpVal.Op())
			}
			switch wantOp := wantOpVal.(type) {
			case *ResponseOpRange:
				gotOpRange, ok := gotOp.(*ResponseOpRange)
				if !ok {
					t.Fatalf("operation i=%d (%s) want type ResponseOpRange but it isn't", i, gotOp.Op())
				}
				if !reflect.DeepEqual(gotOpRange.Range, wantOp.Range) {
					t.Errorf("operation i=%d (%s) got range %v want range %v", i, gotOp.Op(), gotOpRange.Range, wantOp.Range)
				}
				if len(gotOpRange.Rooms) != len(wantOp.Rooms) {
					t.Fatalf("operation i=%d (%s) got %d rooms in array, want %d", i, gotOp.Op(), len(gotOpRange.Rooms), len(wantOp.Rooms))
				}
				for j := range wantOp.Rooms {
					checkRoomsEqual(t, checkRoomIDsOnly, &gotOpRange.Rooms[j], &wantOp.Rooms[j])
				}
			case *ResponseOpSingle:
				gotOpSingle, ok := gotOp.(*ResponseOpSingle)
				if !ok {
					t.Fatalf("operation i=%d (%s) want type ResponseOpSingle but it isn't", i, gotOp.Op())
				}
				if *gotOpSingle.Index != *wantOp.Index {
					t.Errorf("operation i=%d (%s) single op on index %d want index %d", i, gotOp.Op(), *gotOpSingle.Index, *wantOp.Index)
				}
				checkRoomsEqual(t, checkRoomIDsOnly, gotOpSingle.Room, wantOp.Room)
			}
		}
	}
	if len(want.RoomSubscriptions) > 0 {
		if len(want.RoomSubscriptions) != len(got.RoomSubscriptions) {
			t.Errorf("wrong number of room subs returned, got %d want %d", len(got.RoomSubscriptions), len(want.RoomSubscriptions))
		}
		for roomID, wantData := range want.RoomSubscriptions {
			gotData, ok := got.RoomSubscriptions[roomID]
			if !ok {
				t.Errorf("wanted room subscription for %s but it was not returned", roomID)
				continue
			}
			checkRoomsEqual(t, checkRoomIDsOnly, &gotData, &wantData)
		}
	}
}

func checkRoomsEqual(t *testing.T, checkRoomIDsOnly bool, got, want *Room) {
	t.Helper()
	if got == nil && want == nil {
		return // e.g DELETE ops
	}
	if (got == nil && want != nil) || (want == nil && got != nil) {
		t.Fatalf("nil room, got %+v want %+v", got, want)
	}
	if checkRoomIDsOnly {
		if got.RoomID != want.RoomID {
			t.Fatalf("got room '%s' want room '%s'", got.RoomID, want.RoomID)
		}
		return
	}
	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("cannot marshal got room: %s", err)
	}
	wantBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("cannot marshal want room: %s", err)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Errorf("rooms do not match,\ngot  %s want %s", string(gotBytes), string(wantBytes))
	}
}

func serialise(t *testing.T, thing interface{}) string {
	b, err := json.Marshal(thing)
	if err != nil {
		t.Fatalf("cannot serialise: %s", err)
	}
	return string(b)
}

func intPtr(val int) *int {
	return &val
}
