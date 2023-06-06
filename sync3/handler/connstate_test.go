package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/matrix-org/sliding-sync/testutils"
)

type NopExtensionHandler struct{}

func (h *NopExtensionHandler) Handle(ctx context.Context, req extensions.Request, extCtx extensions.Context) (res extensions.Response) {
	return
}

func (h *NopExtensionHandler) HandleLiveUpdate(ctx context.Context, update caches.Update, req extensions.Request, res *extensions.Response, extCtx extensions.Context) {
}

type NopJoinTracker struct{}

func (t *NopJoinTracker) IsUserJoined(userID, roomID string) bool {
	return true
}

type NopTransactionFetcher struct{}

func (t *NopTransactionFetcher) TransactionIDForEvents(userID, deviceID string, eventIDs []string) (eventIDToTxnID map[string]string) {
	return
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
		u.RequestedTimeline = []json.RawMessage{[]byte(`{}`)}
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
	globalCache.LoadJoinedRoomsOverride = func(userID string) (pos int64, joinedRooms map[string]*internal.RoomMetadata, joinTimings map[string]internal.EventMetadata, err error) {
		return 1, map[string]*internal.RoomMetadata{
				roomA.RoomID: &roomA,
				roomB.RoomID: &roomB,
				roomC.RoomID: &roomC,
			}, map[string]internal.EventMetadata{
				roomA.RoomID: {NID: 123, Timestamp: 123},
				roomB.RoomID: {NID: 456, Timestamp: 456},
				roomC.RoomID: {NID: 780, Timestamp: 789},
			}, nil
	}
	userCache := caches.NewUserCache(userID, globalCache, nil, &NopTransactionFetcher{})
	dispatcher.Register(context.Background(), userCache.UserID, userCache)
	dispatcher.Register(context.Background(), sync3.DispatcherAllUsers, globalCache)
	userCache.LazyRoomDataOverride = func(loadPos int64, roomIDs []string, maxTimelineEvents int) map[string]caches.UserRoomData {
		result := make(map[string]caches.UserRoomData)
		for _, roomID := range roomIDs {
			u := caches.NewUserRoomData()
			u.RequestedTimeline = []json.RawMessage{timeline[roomID]}
			result[roomID] = u
		}
		return result
	}
	cs := NewConnState(userID, deviceID, userCache, globalCache, &NopExtensionHandler{}, &NopJoinTracker{}, nil, 1000)
	if userID != cs.UserID() {
		t.Fatalf("UserID returned wrong value, got %v want %v", cs.UserID(), userID)
	}
	res, err := cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
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
		Rooms: map[string]sync3.Room{
			roomB.RoomID: {
				Name:     roomB.NameEvent,
				Initial:  true,
				Timeline: []json.RawMessage{timeline[roomB.RoomID]},
			},
			roomC.RoomID: {
				Name:     roomC.NameEvent,
				Initial:  true,
				Timeline: []json.RawMessage{timeline[roomC.RoomID]},
			},
			roomA.RoomID: {
				Name:     roomA.NameEvent,
				Initial:  true,
				Timeline: []json.RawMessage{timeline[roomA.RoomID]},
			},
		},
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: 3,
				Ops: []sync3.ResponseOp{
					&sync3.ResponseOpRange{
						Operation: "SYNC",
						Range:     [2]int64{0, 2},
						RoomIDs: []string{
							roomB.RoomID, roomC.RoomID, roomA.RoomID,
						},
					},
				},
			},
		},
	})

	// bump A to the top
	newEvent := testutils.NewEvent(t, "unimportant", "me", struct{}{}, testutils.WithTimestamp(timestampNow.Add(1*time.Second)))
	dispatcher.OnNewEvent(context.Background(), roomA.RoomID, newEvent, 1)

	// request again for the diff
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
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
		Rooms: map[string]sync3.Room{
			roomA.RoomID: {
				Timeline: []json.RawMessage{newEvent},
			},
		},
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: 3,
				Ops: []sync3.ResponseOp{
					&sync3.ResponseOpSingle{
						Operation: "DELETE",
						Index:     intPtr(2),
					},
					&sync3.ResponseOpSingle{
						Operation: "INSERT",
						Index:     intPtr(0),
						RoomID:    roomA.RoomID,
					},
				},
			},
		},
	})

	// another message should just update
	newEvent = testutils.NewEvent(t, "unimportant", "me", struct{}{}, testutils.WithTimestamp(timestampNow.Add(2*time.Second)))
	dispatcher.OnNewEvent(context.Background(), roomA.RoomID, newEvent, 2)
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
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
		Rooms: map[string]sync3.Room{
			roomA.RoomID: {
				Timeline: []json.RawMessage{newEvent},
			},
		},
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: 3,
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
	globalCache.LoadJoinedRoomsOverride = func(userID string) (pos int64, joinedRooms map[string]*internal.RoomMetadata, joinTimings map[string]internal.EventMetadata, err error) {
		roomMetadata := make(map[string]*internal.RoomMetadata)
		joinTimings = make(map[string]internal.EventMetadata)
		for i, r := range rooms {
			roomMetadata[r.RoomID] = rooms[i]
			joinTimings[r.RoomID] = internal.EventMetadata{
				NID:       123456, // Dummy values
				Timestamp: 123456,
			}
		}
		return 1, roomMetadata, joinTimings, nil
	}
	userCache := caches.NewUserCache(userID, globalCache, nil, &NopTransactionFetcher{})
	userCache.LazyRoomDataOverride = mockLazyRoomOverride
	dispatcher.Register(context.Background(), userCache.UserID, userCache)
	dispatcher.Register(context.Background(), sync3.DispatcherAllUsers, globalCache)
	cs := NewConnState(userID, deviceID, userCache, globalCache, &NopExtensionHandler{}, &NopJoinTracker{}, nil, 1000)

	// request first page
	res, err := cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
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
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: len(rooms),
				Ops: []sync3.ResponseOp{
					&sync3.ResponseOpRange{
						Operation: "SYNC",
						Range:     [2]int64{0, 2},
						RoomIDs:   []string{roomIDs[0], roomIDs[1], roomIDs[2]},
					},
				},
			},
		},
	})
	// add on a different non-overlapping range
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
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
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: len(rooms),
				Ops: []sync3.ResponseOp{
					&sync3.ResponseOpRange{
						Operation: "SYNC",
						Range:     [2]int64{4, 6},
						RoomIDs:   []string{roomIDs[4], roomIDs[5], roomIDs[6]},
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
	dispatcher.OnNewEvent(context.Background(), roomIDs[8], newEvent, 1)

	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
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
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: len(rooms),
				Ops: []sync3.ResponseOp{
					&sync3.ResponseOpSingle{
						Operation: "DELETE",
						Index:     intPtr(6),
					},
					&sync3.ResponseOpSingle{
						Operation: "INSERT",
						Index:     intPtr(0),
						RoomID:    roomIDs[8],
					},
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
	dispatcher.OnNewEvent(context.Background(), roomIDs[9], newEvent, 1)
	t.Logf("new event %s : %s", roomIDs[9], string(newEvent))
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
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
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: len(rooms),
				Ops: []sync3.ResponseOp{
					&sync3.ResponseOpSingle{
						Operation: "DELETE",
						Index:     intPtr(6),
					},
					&sync3.ResponseOpSingle{
						Operation: "INSERT",
						Index:     intPtr(4),
						RoomID:    roomIDs[2],
					},
				},
			},
		},
	})
}

// Regression test
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
	globalCache.LoadJoinedRoomsOverride = func(userID string) (pos int64, joinedRooms map[string]*internal.RoomMetadata, joinTimings map[string]internal.EventMetadata, err error) {
		return 1, map[string]*internal.RoomMetadata{
				roomA.RoomID: &roomA,
				roomB.RoomID: &roomB,
				roomC.RoomID: &roomC,
				roomD.RoomID: &roomD,
			}, map[string]internal.EventMetadata{
				roomA.RoomID: {NID: 1, Timestamp: 1},
				roomB.RoomID: {NID: 2, Timestamp: 2},
				roomC.RoomID: {NID: 3, Timestamp: 3},
				roomD.RoomID: {NID: 4, Timestamp: 4},
			}, nil

	}
	userCache := caches.NewUserCache(userID, globalCache, nil, &NopTransactionFetcher{})
	userCache.LazyRoomDataOverride = mockLazyRoomOverride
	dispatcher.Register(context.Background(), userCache.UserID, userCache)
	dispatcher.Register(context.Background(), sync3.DispatcherAllUsers, globalCache)
	cs := NewConnState(userID, deviceID, userCache, globalCache, &NopExtensionHandler{}, &NopJoinTracker{}, nil, 1000)
	// Ask for A,B
	res, err := cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
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
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: 4,
				Ops: []sync3.ResponseOp{
					&sync3.ResponseOpRange{
						Operation: "SYNC",
						Range:     [2]int64{0, 1},
						RoomIDs: []string{
							roomA.RoomID, roomB.RoomID,
						},
					},
				},
			},
		},
	})

	// D gets bumped to C's position but it's still outside the range so nothing should happen
	newEvent := testutils.NewEvent(t, "unimportant", "me", struct{}{}, testutils.WithTimestamp(gomatrixserverlib.Timestamp(roomC.LastMessageTimestamp+2).Time()))
	dispatcher.OnNewEvent(context.Background(), roomD.RoomID, newEvent, 1)

	// expire the context after 10ms so we don't wait forevar
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	res, err = cs.OnIncomingRequest(ctx, ConnID, &sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Sort: []string{sync3.SortByRecency},
			Ranges: sync3.SliceRanges([][2]int64{
				{0, 1},
			}),
		}},
	}, false)
	if err != nil {
		t.Fatalf("OnIncomingRequest returned error : %s", err)
	}
	if len(res.Lists["a"].Ops) > 0 {
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
	globalCache.LoadJoinedRoomsOverride = func(userID string) (pos int64, joinedRooms map[string]*internal.RoomMetadata, joinTimings map[string]internal.EventMetadata, err error) {
		return 1, map[string]*internal.RoomMetadata{
				roomA.RoomID: &roomA,
				roomB.RoomID: &roomB,
				roomC.RoomID: &roomC,
				roomD.RoomID: &roomD,
			}, map[string]internal.EventMetadata{
				roomA.RoomID: {NID: 1, Timestamp: 1},
				roomB.RoomID: {NID: 2, Timestamp: 2},
				roomC.RoomID: {NID: 3, Timestamp: 3},
				roomD.RoomID: {NID: 4, Timestamp: 4},
			}, nil
	}
	userCache := caches.NewUserCache(userID, globalCache, nil, &NopTransactionFetcher{})
	userCache.LazyRoomDataOverride = func(loadPos int64, roomIDs []string, maxTimelineEvents int) map[string]caches.UserRoomData {
		result := make(map[string]caches.UserRoomData)
		for _, roomID := range roomIDs {
			u := caches.NewUserRoomData()
			u.RequestedTimeline = []json.RawMessage{timeline[roomID]}
			result[roomID] = u
		}
		return result
	}
	dispatcher.Register(context.Background(), userCache.UserID, userCache)
	dispatcher.Register(context.Background(), sync3.DispatcherAllUsers, globalCache)
	cs := NewConnState(userID, deviceID, userCache, globalCache, &NopExtensionHandler{}, &NopJoinTracker{}, nil, 1000)
	// subscribe to room D
	res, err := cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomD.RoomID: {
				TimelineLimit: 20,
			},
		},
		Lists: map[string]sync3.RequestList{"a": {
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
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: len(roomIDs),
				Ops: []sync3.ResponseOp{
					&sync3.ResponseOpRange{
						Operation: "SYNC",
						Range:     [2]int64{0, 1},
						RoomIDs: []string{
							roomA.RoomID, roomB.RoomID,
						},
					},
				},
			},
		},
		Rooms: map[string]sync3.Room{
			roomA.RoomID: {
				Name:    roomA.NameEvent,
				Initial: true,
				Timeline: []json.RawMessage{
					timeline[roomA.RoomID],
				},
			},
			roomB.RoomID: {
				Name:    roomB.NameEvent,
				Initial: true,
				Timeline: []json.RawMessage{
					timeline[roomB.RoomID],
				},
			},
			roomD.RoomID: {
				Name:    roomD.NameEvent,
				Initial: true,
				Timeline: []json.RawMessage{
					timeline[roomD.RoomID],
				},
			},
		},
	})
	// room D gets a new event but it's so old it doesn't bump to the top of the list
	newEvent := testutils.NewEvent(t, "unimportant", "me", struct{}{}, testutils.WithTimestamp(gomatrixserverlib.Timestamp(timestampNow-20000).Time()))
	dispatcher.OnNewEvent(context.Background(), roomD.RoomID, newEvent, 1)
	// we should get this message even though it's not in the range because we are subscribed to this room.
	res, err = cs.OnIncomingRequest(context.Background(), ConnID, &sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
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
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: len(roomIDs),
			},
		},
		Rooms: map[string]sync3.Room{
			roomD.RoomID: {
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
		Lists: map[string]sync3.RequestList{"a": {
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
		Lists: map[string]sync3.ResponseList{
			"a": {
				Count: len(roomIDs),
			},
		},
		Rooms: map[string]sync3.Room{
			roomC.RoomID: {
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
	if len(got.Lists) != len(want.Lists) {
		t.Errorf("got %v lists, want %v", len(got.Lists), len(want.Lists))
	}
	for listKey, wl := range want.Lists {
		gl, exists := got.Lists[listKey]
		if !exists {
			t.Errorf("no response key for '%s'", listKey)
			continue
		}
		if wl.Count > 0 && gl.Count != wl.Count {
			t.Errorf("response list %v got count %d want %d", listKey, gl.Count, wl.Count)
		}

		if len(wl.Ops) > 0 {
			t.Logf("got %v", serialise(t, gl))
			t.Logf("want %v", serialise(t, wl))
			t.Logf("DEBUG %v", serialise(t, got))
			defer func() {
				t.Helper()
				if !t.Failed() {
					t.Logf("OK!")
				}
			}()
			if len(gl.Ops) != len(wl.Ops) {
				t.Fatalf("got %d ops, want %d", len(gl.Ops), len(wl.Ops))
			}
			for i, wantOpVal := range wl.Ops {
				gotOp := gl.Ops[i]
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
					if len(gotOpRange.RoomIDs) != len(wantOp.RoomIDs) {
						t.Fatalf("operation i=%d (%s) got %d rooms in array, want %d", i, gotOp.Op(), len(gotOpRange.RoomIDs), len(wantOp.RoomIDs))
					}
					for j := range wantOp.RoomIDs {
						checkRoomIDsEqual(t, gotOpRange.RoomIDs[j], wantOp.RoomIDs[j])
					}
				case *sync3.ResponseOpSingle:
					gotOpSingle, ok := gotOp.(*sync3.ResponseOpSingle)
					if !ok {
						t.Fatalf("operation i=%d (%s) want type ResponseOpSingle but it isn't", i, gotOp.Op())
					}
					if *gotOpSingle.Index != *wantOp.Index {
						t.Errorf("operation i=%d (%s) single op on index %d want index %d", i, gotOp.Op(), *gotOpSingle.Index, *wantOp.Index)
					}
					checkRoomIDsEqual(t, gotOpSingle.RoomID, wantOp.RoomID)
				}
			}
		}
	}
	if len(want.Rooms) > 0 {
		if len(want.Rooms) != len(got.Rooms) {
			t.Errorf("wrong number of room subs returned, got %d want %d", len(got.Rooms), len(want.Rooms))
		}
		for wantRoomID := range want.Rooms {
			if _, ok := got.Rooms[wantRoomID]; !ok {
				t.Errorf("wanted room %s in 'rooms' but it wasn't there", wantRoomID)
				continue
			}
			wantTimeline := want.Rooms[wantRoomID].Timeline
			gotTimeline := got.Rooms[wantRoomID].Timeline
			if len(wantTimeline) > 0 {
				if !reflect.DeepEqual(gotTimeline, wantTimeline) {
					t.Errorf("timeline mismatch for room %s:\ngot  %v\nwant %v", wantRoomID, serialise(t, gotTimeline), serialise(t, wantTimeline))
				}
			}
			if want.Rooms[wantRoomID].Initial != got.Rooms[wantRoomID].Initial {
				t.Errorf("'initial' flag mismatch on room %s: got %v want %v", wantRoomID, got.Rooms[wantRoomID].Initial, want.Rooms[wantRoomID].Initial)
			}
		}
		// TODO: check inside room objects
	}
}

func checkRoomIDsEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got room '%s' want room '%s'", got, want)
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
