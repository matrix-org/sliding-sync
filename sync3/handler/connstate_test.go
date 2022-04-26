package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/sync3/caches"
	"github.com/matrix-org/sync-v3/sync3/extensions"
	"github.com/matrix-org/sync-v3/testutils"
)

type NopExtensionHandler struct{}

func (h *NopExtensionHandler) Handle(req extensions.Request, listRoomIDs map[string]struct{}, isInitial bool) (res extensions.Response) {
	return
}

func (h *NopExtensionHandler) HandleLiveUpdate(u caches.Update, req extensions.Request, res *extensions.Response, updateWillReturnResponse, isInitial bool) {
}

type NopJoinTracker struct{}

func (t *NopJoinTracker) IsUserJoined(userID, roomID string) bool {
	return true
}

type NopTransactionFetcher struct{}

func (t *NopTransactionFetcher) TransactionIDForEvent(userID, eventID string) (txnID string) {
	return ""
}

func newRoomMetadata(roomID string, lastMsgTimestamp gomatrixserverlib.Timestamp) internal.RoomMetadata {
	return internal.RoomMetadata{
		RoomID:               roomID,
		NameEvent:            "Room " + roomID,
		LastMessageTimestamp: uint64(lastMsgTimestamp),
	}
}

func mockLazyRoomOverride(loadPos int64, roomIDs []string, maxTimelineEvents int) map[string]caches.UserRoomData {
	result := make(map[string]caches.UserRoomData)
	for _, roomID := range roomIDs {
		u := caches.NewUserRoomData()
		u.Timeline = []json.RawMessage{[]byte(`{}`)}
		result[roomID] = u
	}
	return result
}

// Sync an account with 3 rooms and check that we can grab all rooms and they are sorted correctly initially. Checks
// that basic UPDATE and DELETE/INSERT works when tracking all rooms.
func TestConnStateInitial(t *testing.T) {
	ConnID := sync3.ConnID{
		DeviceID: "d",
	}
	userID := "@TestConnStateInitial_alice:localhost"
	deviceID := "yep"
	timestampNow := gomatrixserverlib.Timestamp(1632131678061).Time()
	// initial sort order B, C, A
	roomA := newRoomMetadata("!a:localhost", gomatrixserverlib.AsTimestamp(timestampNow.Add(-8*time.Second)))
	roomB := newRoomMetadata("!b:localhost", gomatrixserverlib.AsTimestamp(timestampNow))
	roomC := newRoomMetadata("!c:localhost", gomatrixserverlib.AsTimestamp(timestampNow.Add(-4*time.Second)))
	timeline := map[string]json.RawMessage{
		roomA.RoomID: testutils.NewEvent(t, "m.room.message", userID, map[string]interface{}{"body": "a"}),
		roomB.RoomID: testutils.NewEvent(t, "m.room.message", userID, map[string]interface{}{"body": "b"}),
		roomC.RoomID: testutils.NewEvent(t, "m.room.message", userID, map[string]interface{}{"body": "c"}),
	}
	globalCache := caches.NewGlobalCache(nil)
	globalCache.Startup(map[string]internal.RoomMetadata{
		roomA.RoomID: roomA,
		roomB.RoomID: roomB,
		roomC.RoomID: roomC,
	})
	dispatcher := sync3.NewDispatcher()
	dispatcher.Startup(map[string][]string{
		roomA.RoomID: {userID},
		roomB.RoomID: {userID},
		roomC.RoomID: {userID},
	})
	globalCache.LoadJoinedRoomsOverride = func(userID string) (pos int64, joinedRooms map[string]*internal.RoomMetadata, err error) {
		return 1, map[string]*internal.RoomMetadata{
			roomA.RoomID: &roomA,
			roomB.RoomID: &roomB,
			roomC.RoomID: &roomC,
		}, nil
	}
	userCache := caches.NewUserCache(userID, globalCache, nil, &NopTransactionFetcher{})
	dispatcher.Register(userCache.UserID, userCache)
	dispatcher.Register(sync3.DispatcherAllUsers, globalCache)
	userCache.LazyRoomDataOverride = func(loadPos int64, roomIDs []string, maxTimelineEvents int) map[string]caches.UserRoomData {
		result := make(map[string]caches.UserRoomData)
		for _, roomID := range roomIDs {
			u := caches.NewUserRoomData()
			u.Timeline = []json.RawMessage{timeline[roomID]}
			result[roomID] = u
		}
		return result
	}
	cs := NewConnState(userID, deviceID, userCache, globalCache, &NopExtensionHandler{}, &NopJoinTracker{})
	if userID != cs.UserID() {
		t.Fatalf("UserID returned wrong value, got %v want %v", cs.UserID(), userID)
	}
	res, err := cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 9},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, false, res, &sync3.Response{
		Counts: []int{3},
		Ops: []sync3.ResponseOp{
			&sync3.ResponseOpRange{
				Operation: "SYNC",
				Range:     []int64{0, 9},
				Rooms: []sync3.Room{
					{
						RoomID:   roomB.RoomID,
						Name:     roomB.NameEvent,
						Initial:  true,
						Timeline: []json.RawMessage{timeline[roomB.RoomID]},
					},
					{
						RoomID:   roomC.RoomID,
						Name:     roomC.NameEvent,
						Initial:  true,
						Timeline: []json.RawMessage{timeline[roomC.RoomID]},
					},
					{
						RoomID:   roomA.RoomID,
						Name:     roomA.NameEvent,
						Initial:  true,
						Timeline: []json.RawMessage{timeline[roomA.RoomID]},
					},
				},
			},
		},
	})

	// bump A to the top
	newEvent := testutils.NewEvent(t, "unimportant", "me", struct{}{}, testutils.WithTimestamp(timestampNow.Add(1*time.Second)))
	dispatcher.OnNewEvents(roomA.RoomID, []json.RawMessage{
		newEvent,
	}, 1)

	// request again for the diff
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 9},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &sync3.Response{
		Counts: []int{3},
		Ops: []sync3.ResponseOp{
			&sync3.ResponseOpSingle{
				Operation: "DELETE",
				Index:     intPtr(2),
			},
			&sync3.ResponseOpSingle{
				Operation: "INSERT",
				Index:     intPtr(0),
				Room: &sync3.Room{
					RoomID: roomA.RoomID,
				},
			},
		},
	})

	// another message should just update
	newEvent = testutils.NewEvent(t, "unimportant", "me", struct{}{}, testutils.WithTimestamp(timestampNow.Add(2*time.Second)))
	dispatcher.OnNewEvents(roomA.RoomID, []json.RawMessage{
		newEvent,
	}, 1)
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 9},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &sync3.Response{
		Counts: []int{3},
		Ops: []sync3.ResponseOp{
			&sync3.ResponseOpSingle{
				Operation: "UPDATE",
				Index:     intPtr(0),
				Room: &sync3.Room{
					RoomID: roomA.RoomID,
				},
			},
		},
	})
}

// Test that multiple ranges can be tracked in a single request
func TestConnStateMultipleRanges(t *testing.T) {
	t.Skip("flakey")
	ConnID := sync3.ConnID{
		DeviceID: "d",
	}
	userID := "@TestConnStateMultipleRanges_alice:localhost"
	deviceID := "yep"
	timestampNow := gomatrixserverlib.Timestamp(1632131678061)
	var rooms []*internal.RoomMetadata
	var roomIDs []string
	globalCache := caches.NewGlobalCache(nil)
	dispatcher := sync3.NewDispatcher()
	roomIDToRoom := make(map[string]internal.RoomMetadata)
	for i := int64(0); i < 10; i++ {
		roomID := fmt.Sprintf("!%d:localhost", i)
		room := internal.RoomMetadata{
			RoomID:    roomID,
			NameEvent: fmt.Sprintf("Room %d", i),
			// room 1 is most recent, 10 is least recent
			LastMessageTimestamp: uint64(uint64(timestampNow) - uint64(i*1000)),
		}
		rooms = append(rooms, &room)
		roomIDs = append(roomIDs, roomID)
		roomIDToRoom[roomID] = room
		globalCache.Startup(map[string]internal.RoomMetadata{
			room.RoomID: room,
		})
		dispatcher.Startup(map[string][]string{
			roomID: {userID},
		})
	}
	globalCache.LoadJoinedRoomsOverride = func(userID string) (pos int64, joinedRooms map[string]*internal.RoomMetadata, err error) {
		res := make(map[string]*internal.RoomMetadata)
		for i, r := range rooms {
			res[r.RoomID] = rooms[i]
		}
		return 1, res, nil
	}
	userCache := caches.NewUserCache(userID, globalCache, nil, &NopTransactionFetcher{})
	userCache.LazyRoomDataOverride = mockLazyRoomOverride
	dispatcher.Register(userCache.UserID, userCache)
	dispatcher.Register(sync3.DispatcherAllUsers, globalCache)
	cs := NewConnState(userID, deviceID, userCache, globalCache, &NopExtensionHandler{}, &NopJoinTracker{})

	// request first page
	res, err := cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 2},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &sync3.Response{
		Counts: []int{(len(rooms))},
		Ops: []sync3.ResponseOp{
			&sync3.ResponseOpRange{
				Operation: "SYNC",
				Range:     []int64{0, 2},
				Rooms: []sync3.Room{
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
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 2}, {4, 6},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &sync3.Response{
		Counts: []int{len(rooms)},
		Ops: []sync3.ResponseOp{
			&sync3.ResponseOpRange{
				Operation: "SYNC",
				Range:     []int64{4, 6},
				Rooms: []sync3.Room{
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
	newEvent := testutils.NewEvent(t, "unimportant", "me", struct{}{}, testutils.WithTimestamp(timestampNow.Time().Add(2*time.Second)))
	dispatcher.OnNewEvents(roomIDs[8], []json.RawMessage{
		newEvent,
	}, 1)

	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 2}, {4, 6},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &sync3.Response{
		Counts: []int{len(rooms)},
		Ops: []sync3.ResponseOp{
			&sync3.ResponseOpSingle{
				Operation: "DELETE",
				Index:     intPtr(6),
			},
			&sync3.ResponseOpSingle{
				Operation: "INSERT",
				Index:     intPtr(0),
				Room: &sync3.Room{
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
	newEvent = testutils.NewEvent(t, "unimportant", "me", struct{}{}, testutils.WithTimestamp(gomatrixserverlib.Timestamp(middleTimestamp).Time()))
	dispatcher.OnNewEvents(roomIDs[9], []json.RawMessage{
		newEvent,
	}, 1)
	t.Logf("new event %s : %s", roomIDs[9], string(newEvent))
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 2}, {4, 6},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &sync3.Response{
		Counts: []int{len(rooms)},
		Ops: []sync3.ResponseOp{
			&sync3.ResponseOpSingle{
				Operation: "DELETE",
				Index:     intPtr(6),
			},
			&sync3.ResponseOpSingle{
				Operation: "INSERT",
				Index:     intPtr(4),
				Room: &sync3.Room{
					RoomID: roomIDs[2],
				},
			},
		},
	})
}

// Regression test for https://github.com/matrix-org/sync-v3/commit/732ea46f1ccde2b6a382e0f849bbd166b80900ed
func TestBumpToOutsideRange(t *testing.T) {
	ConnID := sync3.ConnID{
		DeviceID: "d",
	}
	userID := "@TestBumpToOutsideRange_alice:localhost"
	deviceID := "yep"
	timestampNow := gomatrixserverlib.Timestamp(1632131678061)
	roomA := newRoomMetadata("!a:localhost", timestampNow)
	roomB := newRoomMetadata("!b:localhost", timestampNow-1000)
	roomC := newRoomMetadata("!c:localhost", timestampNow-2000)
	roomD := newRoomMetadata("!d:localhost", timestampNow-3000)
	globalCache := caches.NewGlobalCache(nil)
	globalCache.Startup(map[string]internal.RoomMetadata{
		roomA.RoomID: roomA,
		roomB.RoomID: roomB,
		roomC.RoomID: roomC,
		roomD.RoomID: roomD,
	})
	dispatcher := sync3.NewDispatcher()
	dispatcher.Startup(map[string][]string{
		roomA.RoomID: {userID},
		roomB.RoomID: {userID},
		roomC.RoomID: {userID},
		roomD.RoomID: {userID},
	})
	globalCache.LoadJoinedRoomsOverride = func(userID string) (pos int64, joinedRooms map[string]*internal.RoomMetadata, err error) {
		return 1, map[string]*internal.RoomMetadata{
			roomA.RoomID: &roomA,
			roomB.RoomID: &roomB,
			roomC.RoomID: &roomC,
			roomD.RoomID: &roomD,
		}, nil
	}
	userCache := caches.NewUserCache(userID, globalCache, nil, &NopTransactionFetcher{})
	userCache.LazyRoomDataOverride = mockLazyRoomOverride
	dispatcher.Register(userCache.UserID, userCache)
	dispatcher.Register(sync3.DispatcherAllUsers, globalCache)
	cs := NewConnState(userID, deviceID, userCache, globalCache, &NopExtensionHandler{}, &NopJoinTracker{})
	// Ask for A,B
	res, err := cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 1},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &sync3.Response{
		Counts: []int{4},
		Ops: []sync3.ResponseOp{
			&sync3.ResponseOpRange{
				Operation: "SYNC",
				Range:     []int64{0, 1},
				Rooms: []sync3.Room{
					{
						RoomID: roomA.RoomID,
					},
					{
						RoomID: roomB.RoomID,
					},
				},
			},
		},
	})

	// D gets bumped to C's position but it's still outside the range so nothing should happen
	newEvent := testutils.NewEvent(t, "unimportant", "me", struct{}{}, testutils.WithTimestamp(gomatrixserverlib.Timestamp(roomC.LastMessageTimestamp+2).Time()))
	dispatcher.OnNewEvents(roomD.RoomID, []json.RawMessage{
		newEvent,
	}, 1)

	// expire the context after 10ms so we don't wait forevar
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	res, err = cs.OnIncomingRequest(ctx, ConnID, &sync3.Request{
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 1},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	if len(res.Ops) > 0 {
		t.Errorf("response returned ops, expected none")
	}
}

// Test that room subscriptions can be made and that events are pushed for them.
func TestConnStateRoomSubscriptions(t *testing.T) {
	ConnID := sync3.ConnID{
		DeviceID: "d",
	}
	userID := "@TestConnStateRoomSubscriptions_alice:localhost"
	deviceID := "yep"
	timestampNow := gomatrixserverlib.Timestamp(1632131678061)
	roomA := newRoomMetadata("!a:localhost", timestampNow)
	roomB := newRoomMetadata("!b:localhost", gomatrixserverlib.Timestamp(timestampNow-1000))
	roomC := newRoomMetadata("!c:localhost", gomatrixserverlib.Timestamp(timestampNow-2000))
	roomD := newRoomMetadata("!d:localhost", gomatrixserverlib.Timestamp(timestampNow-3000))
	roomIDs := []string{roomA.RoomID, roomB.RoomID, roomC.RoomID, roomD.RoomID}
	globalCache := caches.NewGlobalCache(nil)
	globalCache.Startup(map[string]internal.RoomMetadata{
		roomA.RoomID: roomA,
		roomB.RoomID: roomB,
		roomC.RoomID: roomC,
		roomD.RoomID: roomD,
	})
	dispatcher := sync3.NewDispatcher()
	dispatcher.Startup(map[string][]string{
		roomA.RoomID: {userID},
		roomB.RoomID: {userID},
		roomC.RoomID: {userID},
		roomD.RoomID: {userID},
	})
	timeline := map[string]json.RawMessage{
		roomA.RoomID: testutils.NewEvent(t, "m.room.message", userID, map[string]interface{}{"body": "a"}),
		roomB.RoomID: testutils.NewEvent(t, "m.room.message", userID, map[string]interface{}{"body": "b"}),
		roomC.RoomID: testutils.NewEvent(t, "m.room.message", userID, map[string]interface{}{"body": "c"}),
		roomD.RoomID: testutils.NewEvent(t, "m.room.message", userID, map[string]interface{}{"body": "d"}),
	}
	globalCache.LoadJoinedRoomsOverride = func(userID string) (pos int64, joinedRooms map[string]*internal.RoomMetadata, err error) {
		return 1, map[string]*internal.RoomMetadata{
			roomA.RoomID: &roomA,
			roomB.RoomID: &roomB,
			roomC.RoomID: &roomC,
			roomD.RoomID: &roomD,
		}, nil
	}
	userCache := caches.NewUserCache(userID, globalCache, nil, &NopTransactionFetcher{})
	userCache.LazyRoomDataOverride = func(loadPos int64, roomIDs []string, maxTimelineEvents int) map[string]caches.UserRoomData {
		result := make(map[string]caches.UserRoomData)
		for _, roomID := range roomIDs {
			u := caches.NewUserRoomData()
			u.Timeline = []json.RawMessage{timeline[roomID]}
			result[roomID] = u
		}
		return result
	}
	dispatcher.Register(userCache.UserID, userCache)
	dispatcher.Register(sync3.DispatcherAllUsers, globalCache)
	cs := NewConnState(userID, deviceID, userCache, globalCache, &NopExtensionHandler{}, &NopJoinTracker{})
	// subscribe to room D
	res, err := cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomD.RoomID: {
				TimelineLimit: 20,
			},
		},
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 1},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, false, res, &sync3.Response{
		Counts: []int{len(roomIDs)},
		RoomSubscriptions: map[string]sync3.Room{
			roomD.RoomID: {
				RoomID:  roomD.RoomID,
				Name:    roomD.NameEvent,
				Initial: true,
				Timeline: []json.RawMessage{
					timeline[roomD.RoomID],
				},
			},
		},
		Ops: []sync3.ResponseOp{
			&sync3.ResponseOpRange{
				Operation: "SYNC",
				Range:     []int64{0, 1},
				Rooms: []sync3.Room{
					{
						RoomID:  roomA.RoomID,
						Name:    roomA.NameEvent,
						Initial: true,
						Timeline: []json.RawMessage{
							timeline[roomA.RoomID],
						},
					},
					{
						RoomID:  roomB.RoomID,
						Name:    roomB.NameEvent,
						Initial: true,
						Timeline: []json.RawMessage{
							timeline[roomB.RoomID],
						},
					},
				},
			},
		},
	})
	// room D gets a new event
	newEvent := testutils.NewEvent(t, "unimportant", "me", struct{}{}, testutils.WithTimestamp(gomatrixserverlib.Timestamp(timestampNow+2000).Time()))
	dispatcher.OnNewEvents(roomD.RoomID, []json.RawMessage{
		newEvent,
	}, 1)
	// we should get this message even though it's not in the range because we are subscribed to this room.
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 1},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, false, res, &sync3.Response{
		Counts: []int{len(roomIDs)},
		RoomSubscriptions: map[string]sync3.Room{
			roomD.RoomID: {
				RoomID: roomD.RoomID,
				Timeline: []json.RawMessage{
					newEvent,
				},
			},
		},
		// TODO: index markers as this new event should bump D into the tracked range
	})

	// now swap to room C
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomC.RoomID: {
				TimelineLimit: 20,
			},
		},
		UnsubscribeRooms: []string{roomD.RoomID},
		Lists: []sync3.RequestList{{
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 1},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	checkResponse(t, false, res, &sync3.Response{
		Counts: []int{len(roomIDs)},
		RoomSubscriptions: map[string]sync3.Room{
			roomC.RoomID: {
				RoomID:  roomC.RoomID,
				Name:    roomC.NameEvent,
				Initial: true,
				Timeline: []json.RawMessage{
					timeline[roomC.RoomID],
				},
			},
		},
	})
}

func checkResponse(t *testing.T, checkRoomIDsOnly bool, got, want *sync3.Response) {
	t.Helper()
	if len(want.Counts) > 0 {
		if !reflect.DeepEqual(got.Counts, want.Counts) {
			t.Errorf("response Counts: got %v want %v", got.Counts, want.Counts)
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
			case *sync3.ResponseOpRange:
				gotOpRange, ok := gotOp.(*sync3.ResponseOpRange)
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
			case *sync3.ResponseOpSingle:
				gotOpSingle, ok := gotOp.(*sync3.ResponseOpSingle)
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

func checkRoomsEqual(t *testing.T, checkRoomIDsOnly bool, got, want *sync3.Room) {
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
