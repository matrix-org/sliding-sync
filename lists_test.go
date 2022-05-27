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

	MatchResponse(t, res,
		MatchV3Counts([]int{len(encryptedRooms), len(unencryptedRooms)}),
		MatchV3Ops(0, MatchV3SyncOpFn(func(op *sync3.ResponseOpRange) error {
			// first 3 encrypted rooms
			return checkRoomList(res, op, encryptedRooms[:3])
		})),
		MatchV3Ops(1, MatchV3SyncOpFn(func(op *sync3.ResponseOpRange) error {
			// first 3 unencrypted rooms
			return checkRoomList(res, op, unencryptedRooms[:3])
		})),
	)

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
	MatchResponse(t, res, MatchV3Counts([]int{len(encryptedRooms), len(unencryptedRooms)}), MatchV3Ops(1,
		MatchV3SyncOpFn(func(op *sync3.ResponseOpRange) error {
			return checkRoomList(res, op, unencryptedRooms[3:6])
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
	MatchResponse(t, res, MatchV3Counts([]int{len(encryptedRooms), len(unencryptedRooms)}), MatchV3Ops(0,
		MatchV3DeleteOp(2),
		MatchV3InsertOp(0, encryptedRooms[0].roomID),
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

	MatchResponse(t, res,
		MatchV3Counts([]int{len(dmRooms), len(groupRooms)}),
		MatchV3Ops(0, MatchV3SyncOpFn(func(op *sync3.ResponseOpRange) error {
			// first 3 DM rooms
			return checkRoomList(res, op, dmRooms[:3])
		})),
		MatchV3Ops(1, MatchV3SyncOpFn(func(op *sync3.ResponseOpRange) error {
			// first 3 group rooms
			return checkRoomList(res, op, groupRooms[:3])
		})),
	)

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
	MatchResponse(t, res, MatchV3Counts([]int{len(dmRooms), len(groupRooms)}),
		MatchV3Ops(
			0, MatchV3DeleteOp(2),
			MatchV3InsertOp(0, dmRooms[0].roomID),
		),
		MatchRoomSubscription(dmRooms[0].roomID, MatchRoomHighlightCount(1), MatchRoomTimelineMostRecent(1, dmRooms[0].events)),
	)
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
	MatchResponse(t, res, MatchV3Counts([]int{len(allRooms)}), MatchV3Ops(0,
		MatchV3SyncOpFn(func(op *sync3.ResponseOpRange) error {
			return checkRoomList(res, op, allRooms[0:3])
		}),
	))
}

// Tests that if a room appears in >1 list that we union room subscriptions correctly.
func TestMultipleOverlappingLists(t *testing.T) {
	boolTrue := true
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	var allRooms []roomEvents
	var encryptedRooms []roomEvents
	var encryptedRoomIDs []string
	var dmRooms []roomEvents
	var dmRoomIDs []string
	dmContent := map[string][]string{} // user_id -> [room_id]
	dmUser := bob
	baseTimestamp := time.Now()
	// make 3 encrypted rooms, 3 encrypted/dm rooms, 3 dm rooms.
	for i := 0; i < 9; i++ {
		isEncrypted := i < 6
		isDM := i >= 3
		ts := baseTimestamp.Add(time.Duration(-1*i) * time.Second)
		room := roomEvents{
			roomID: fmt.Sprintf("!room_%d:localhost", i),
			events: createRoomState(t, alice, ts),
		}
		if isEncrypted {
			room.events = append(room.events, testutils.NewStateEvent(
				t, "m.room.encryption", "", alice, map[string]interface{}{
					"algorithm":            "m.megolm.v1.aes-sha2",
					"rotation_period_ms":   604800000,
					"rotation_period_msgs": 100,
				}, testutils.WithTimestamp(ts),
			))
		}
		if isDM {
			dmContent[dmUser] = append(dmContent[dmUser], room.roomID)
		}
		allRooms = append(allRooms, room)
		if isEncrypted {
			encryptedRooms = append(encryptedRooms, room)
			encryptedRoomIDs = append(encryptedRoomIDs, room.roomID)
		}
		if isDM {
			dmRooms = append(dmRooms, room)
			dmRoomIDs = append(dmRoomIDs, room.roomID)
		}
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
	// request 2 lists: one DMs, one encrypted. The room subscriptions are different so they should be UNION'd correctly.
	// We request 5 rooms to ensure there is some overlap but not total overlap:
	//   newest     top 5 DM
	//   v     .-------------.
	//   E E E ED* ED* ED* D D D
	//   `-----------`
	//      top 5 Encrypted
	//
	// Rooms with * are union'd
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 4}, // first 5 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 3,
					RequiredState: [][2]string{
						{"m.room.join_rules", ""},
					},
				},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolTrue,
				},
			},
			{
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 4}, // first 5 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
					RequiredState: [][2]string{
						{"m.room.power_levels", ""},
					},
				},
				Filters: &sync3.RequestFilters{
					IsDM: &boolTrue,
				},
			},
		},
	})

	MatchResponse(t, res,
		MatchV3Ops(0, MatchV3SyncOp(0, 4, encryptedRoomIDs[:5])),
		MatchV3Ops(1, MatchV3SyncOp(0, 4, dmRoomIDs[:5])),
		MatchRoomSubscriptions(map[string][]roomMatcher{
			// encrypted rooms just come from the encrypted only list
			encryptedRoomIDs[0]: {
				MatchRoomID(encryptedRoomIDs[0]),
				MatchRoomInitial(true),
				MatchRoomRequiredState([]json.RawMessage{
					encryptedRooms[0].getStateEvent("m.room.join_rules", ""),
				}),
				MatchRoomTimelineMostRecent(3, encryptedRooms[0].events),
			},
			encryptedRoomIDs[1]: {
				MatchRoomID(encryptedRoomIDs[1]),
				MatchRoomInitial(true),
				MatchRoomRequiredState([]json.RawMessage{
					encryptedRooms[1].getStateEvent("m.room.join_rules", ""),
				}),
				MatchRoomTimelineMostRecent(3, encryptedRooms[1].events),
			},
			encryptedRoomIDs[2]: {
				MatchRoomID(encryptedRoomIDs[2]),
				MatchRoomInitial(true),
				MatchRoomRequiredState([]json.RawMessage{
					encryptedRooms[2].getStateEvent("m.room.join_rules", ""),
				}),
				MatchRoomTimelineMostRecent(3, encryptedRooms[2].events),
			},
			// overlapping with DM rooms
			encryptedRoomIDs[3]: {
				MatchRoomID(encryptedRoomIDs[3]),
				MatchRoomInitial(true),
				MatchRoomRequiredState([]json.RawMessage{
					encryptedRooms[3].getStateEvent("m.room.join_rules", ""),
					encryptedRooms[3].getStateEvent("m.room.power_levels", ""),
				}),
				MatchRoomTimelineMostRecent(3, encryptedRooms[3].events),
			},
			encryptedRoomIDs[4]: {
				MatchRoomID(encryptedRoomIDs[4]),
				MatchRoomInitial(true),
				MatchRoomRequiredState([]json.RawMessage{
					encryptedRooms[4].getStateEvent("m.room.join_rules", ""),
					encryptedRooms[4].getStateEvent("m.room.power_levels", ""),
				}),
				MatchRoomTimelineMostRecent(3, encryptedRooms[4].events),
			},
			// DM only rooms
			dmRoomIDs[2]: {
				MatchRoomID(dmRoomIDs[2]),
				MatchRoomInitial(true),
				MatchRoomRequiredState([]json.RawMessage{
					dmRooms[2].getStateEvent("m.room.power_levels", ""),
				}),
				MatchRoomTimelineMostRecent(1, dmRooms[2].events),
			},
			dmRoomIDs[3]: {
				MatchRoomID(dmRoomIDs[3]),
				MatchRoomInitial(true),
				MatchRoomRequiredState([]json.RawMessage{
					dmRooms[3].getStateEvent("m.room.power_levels", ""),
				}),
				MatchRoomTimelineMostRecent(1, dmRooms[3].events),
			},
			dmRoomIDs[4]: {
				MatchRoomID(dmRoomIDs[4]),
				MatchRoomInitial(true),
				MatchRoomRequiredState([]json.RawMessage{
					dmRooms[4].getStateEvent("m.room.power_levels", ""),
				}),
				MatchRoomTimelineMostRecent(1, dmRooms[4].events),
			},
		}),
	)

}

// Check that the range op matches all the wantRooms
func checkRoomList(res *sync3.Response, op *sync3.ResponseOpRange, wantRooms []roomEvents) error {
	if len(op.RoomIDs) != len(wantRooms) {
		return fmt.Errorf("want %d rooms, got %d", len(wantRooms), len(op.RoomIDs))
	}
	for i := range wantRooms {
		err := wantRooms[i].MatchRoom(
			res.Rooms[op.RoomIDs[i]],
			MatchRoomTimelineMostRecent(1, wantRooms[i].events),
		)
		if err != nil {
			return err
		}
	}
	return nil
}
