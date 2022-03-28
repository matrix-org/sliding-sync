package syncv3

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
)

// Test that multiple lists can be independently scrolled through
func TestMultipleLists(t *testing.T) {
	boolTrue := true
	boolFalse := false
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	var allRooms []roomEvents
	var encryptedRooms []roomEvents
	var unencryptedRooms []roomEvents
	baseTimestamp := time.Now()
	// make 10 encrypted rooms and make 10 unencrypted rooms. Room 0 is most recent to ease checks
	for i := 0; i < 10; i++ {
		ts := baseTimestamp.Add(time.Duration(-1*i) * time.Second)
		encRoom := roomEvents{
			roomID: fmt.Sprintf("!encrypted_%d:localhost", i),
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(
					t, "m.room.encryption", "", alice, map[string]interface{}{
						"algorithm":            "m.megolm.v1.aes-sha2",
						"rotation_period_ms":   604800000,
						"rotation_period_msgs": 100,
					}, testutils.WithTimestamp(ts),
				),
			}...),
		}
		room := roomEvents{
			roomID: fmt.Sprintf("!unencrypted_%d:localhost", i),
			events: createRoomState(t, alice, ts),
		}
		allRooms = append(allRooms, []roomEvents{encRoom, room}...)
		encryptedRooms = append(encryptedRooms, encRoom)
		unencryptedRooms = append(unencryptedRooms, room)
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// request 2 lists, one set encrypted, one set unencrypted
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolTrue,
				},
			},
			{
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolFalse,
				},
			},
		},
	})

	seen := map[int]bool{}
	opMatch := func(op *sync3.ResponseOpRange) error {
		seen[op.List] = true
		if op.List == 0 { // first 3 encrypted rooms
			return checkRoomList(op, encryptedRooms[:3])
		} else if op.List == 1 { // first 3 unencrypted rooms
			return checkRoomList(op, unencryptedRooms[:3])
		}
		return fmt.Errorf("unknown List: %d", op.List)
	}

	MatchResponse(t, res, MatchV3Counts([]int{len(encryptedRooms), len(unencryptedRooms)}), MatchV3Ops(
		MatchV3SyncOp(opMatch), MatchV3SyncOp(opMatch),
	))

	if !seen[0] || !seen[1] {
		t.Fatalf("didn't see both list 0 and 1: %+v", res)
	}

	// now scroll one of the lists

	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms still
				},
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
					[2]int64{3, 5}, // next 3 rooms
				},
			},
		},
	})
	MatchResponse(t, res, MatchV3Counts([]int{len(encryptedRooms), len(unencryptedRooms)}), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if op.List != 1 {
				return fmt.Errorf("expected unencrypted list to be SYNCed but wasn't, got list %d want %d", op.List, 1)
			}
			return checkRoomList(op, unencryptedRooms[3:6])
		}),
	))

	// now shift the last/oldest unencrypted room to an encrypted room and make sure both lists update
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				unencryptedRooms[len(unencryptedRooms)-1].roomID: {
					Timeline: sync2.TimelineResponse{
						Events: []json.RawMessage{
							testutils.NewStateEvent(
								t, "m.room.encryption", "", alice, map[string]interface{}{
									"algorithm":            "m.megolm.v1.aes-sha2",
									"rotation_period_ms":   604800000,
									"rotation_period_msgs": 100,
								},
							),
						},
					},
				},
			},
		},
	})
	v2.waitUntilEmpty(t, alice)
	// update our source of truth: the last unencrypted room is now the first encrypted room
	encryptedRooms = append([]roomEvents{unencryptedRooms[len(unencryptedRooms)-1]}, encryptedRooms...)
	unencryptedRooms = unencryptedRooms[:len(unencryptedRooms)-1]

	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms still
				},
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
					[2]int64{3, 5}, // next 3 rooms
				},
			},
		},
	})
	// We are tracking the first few encrypted rooms so we expect list 0 to update
	// However we do not track old unencrypted rooms so we expect no change in list 1
	// TODO: We always assume operations are done sequentially starting at list 0, is this safe?
	MatchResponse(t, res, MatchV3Counts([]int{len(encryptedRooms), len(unencryptedRooms)}), MatchV3Ops(
		MatchV3DeleteOp(0, 2),
		MatchV3InsertOp(0, 0, encryptedRooms[0].roomID),
	))
}

// Test that highlights / bumps only update a single list and not both. Regression test for when
// DM rooms get bumped they appeared in the is_dm:false list.
func TestMultipleListsDMUpdate(t *testing.T) {
	boolTrue := true
	boolFalse := false
	one := 1
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	var allRooms []roomEvents
	var dmRooms []roomEvents
	var groupRooms []roomEvents
	baseTimestamp := time.Now()
	dmContent := map[string][]string{} // user_id -> [room_id]
	// make 10 group rooms and make 10 DMs rooms. Room 0 is most recent to ease checks
	for i := 0; i < 10; i++ {
		ts := baseTimestamp.Add(time.Duration(-1*i) * time.Second)
		dmUser := fmt.Sprintf("@dm_%d:localhost", i)
		dmRoomID := fmt.Sprintf("!dm_%d:localhost", i)
		dmRoom := roomEvents{
			roomID: dmRoomID,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(
					t, "m.room.member", dmUser, dmUser, map[string]interface{}{
						"membership": "join",
					}, testutils.WithTimestamp(ts),
				),
			}...),
		}
		groupRoom := roomEvents{
			roomID: fmt.Sprintf("!group_%d:localhost", i),
			events: createRoomState(t, alice, ts),
		}
		allRooms = append(allRooms, []roomEvents{dmRoom, groupRoom}...)
		dmRooms = append(dmRooms, dmRoom)
		groupRooms = append(groupRooms, groupRoom)
		dmContent[dmUser] = []string{dmRoomID}
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		AccountData: sync2.EventsResponse{
			Events: []json.RawMessage{
				testutils.NewEvent(t, "m.direct", alice, dmContent),
			},
		},
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// request 2 lists, one set DM, one set no DM
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
				Filters: &sync3.RequestFilters{
					IsDM: &boolTrue,
				},
			},
			{
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
				Filters: &sync3.RequestFilters{
					IsDM: &boolFalse,
				},
			},
		},
	})

	seen := map[int]bool{}
	opMatch := func(op *sync3.ResponseOpRange) error {
		seen[op.List] = true
		if op.List == 0 { // first 3 DM rooms
			return checkRoomList(op, dmRooms[:3])
		} else if op.List == 1 { // first 3 group rooms
			return checkRoomList(op, groupRooms[:3])
		}
		return fmt.Errorf("unknown List: %d", op.List)
	}

	MatchResponse(t, res, MatchV3Counts([]int{len(dmRooms), len(groupRooms)}), MatchV3Ops(
		MatchV3SyncOp(opMatch), MatchV3SyncOp(opMatch),
	))

	// now bring the last DM room to the top with a notif
	pingMessage := testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "ping"})
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				dmRooms[len(dmRooms)-1].roomID: {
					UnreadNotifications: sync2.UnreadNotifications{
						HighlightCount: &one,
					},
					Timeline: sync2.TimelineResponse{
						Events: []json.RawMessage{
							pingMessage,
						},
					},
				},
			},
		},
	})
	v2.waitUntilEmpty(t, alice)
	// update our source of truth
	dmRooms = append([]roomEvents{dmRooms[len(dmRooms)-1]}, dmRooms[1:]...)
	dmRooms[0].events = append(dmRooms[0].events, pingMessage)

	// now get the delta: only the DM room should change
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms still
				},
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms still
				},
			},
		},
	})
	MatchResponse(t, res, MatchV3Counts([]int{len(dmRooms), len(groupRooms)}), MatchV3Ops(
		MatchV3DeleteOp(0, 2),
		MatchV3InsertOp(0, 0, dmRooms[0].roomID, MatchRoomHighlightCount(1), MatchRoomTimelineMostRecent(1, dmRooms[0].events)),
	))
}

// Test that a new list can be added mid-connection
func TestNewListMidConnection(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	var allRooms []roomEvents
	baseTimestamp := time.Now()
	// make 10 rooms
	for i := 0; i < 10; i++ {
		ts := baseTimestamp.Add(time.Duration(-1*i) * time.Second)
		room := roomEvents{
			roomID: fmt.Sprintf("!%d:localhost", i),
			events: createRoomState(t, alice, ts),
		}
		allRooms = append(allRooms, room)
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// first request no list
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{},
	})

	MatchResponse(t, res, MatchV3Counts([]int{}))

	// now add a list
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
			},
		},
	})
	MatchResponse(t, res, MatchV3Counts([]int{len(allRooms)}), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if op.List != 0 {
				return fmt.Errorf("got list %d want %d", op.List, 0)
			}
			return checkRoomList(op, allRooms[0:3])
		}),
	))
}

// Check that the range op matches all the wantRooms
func checkRoomList(op *sync3.ResponseOpRange, wantRooms []roomEvents) error {
	if len(op.Rooms) != len(wantRooms) {
		return fmt.Errorf("want %d rooms, got %d", len(wantRooms), len(op.Rooms))
	}
	for i := range wantRooms {
		err := wantRooms[i].MatchRoom(
			op.Rooms[i],
			MatchRoomTimelineMostRecent(1, wantRooms[i].events),
		)
		if err != nil {
			return err
		}
	}
	return nil
}
