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

// Test that filters work initially and whilst streamed.
func TestFiltersEncryption(t *testing.T) {
	boolTrue := true
	boolFalse := false
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	encryptedRoomID := "!TestFilters_encrypted:localhost"
	unencryptedRoomID := "!TestFilters_unencrypted:localhost"
	latestTimestamp := time.Now()
	allRooms := []roomEvents{
		// make an encrypted room and an unencrypted room
		{
			roomID: encryptedRoomID,
			events: append(createRoomState(t, alice, latestTimestamp), []json.RawMessage{
				testutils.NewStateEvent(
					t, "m.room.encryption", "", alice, map[string]interface{}{
						"algorithm":            "m.megolm.v1.aes-sha2",
						"rotation_period_ms":   604800000,
						"rotation_period_msgs": 100,
					},
				),
			}...),
		},
		{
			roomID: unencryptedRoomID,
			events: createRoomState(t, alice, latestTimestamp),
		},
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// connect and make sure either the encrypted room or not depending on what the filter says
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolTrue,
				},
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolFalse,
				},
			},
		},
	})
	MatchResponse(t, res, MatchV3Counts([]int{1, 1}), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != 1 {
				return fmt.Errorf("want %d rooms, got %d", 1, len(op.Rooms))
			}
			if op.List != 0 {
				return fmt.Errorf("unknown list: %d", op.List)
			}
			return allRooms[0].MatchRoom(op.Rooms[0]) // encrypted room
		}),
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != 1 {
				return fmt.Errorf("want %d rooms, got %d", 1, len(op.Rooms))
			}
			if op.List != 1 {
				return fmt.Errorf("unknown list: %d", op.List)
			}
			return allRooms[1].MatchRoom(op.Rooms[0]) // encrypted room
		}),
	))

	// change the unencrypted room into an encrypted room
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				unencryptedRoomID: {
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

	// now requesting the encrypted list should include it (added)
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
				},
				// sticky; should remember filters
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
				},
				// sticky; should remember filters
			},
		},
	})
	MatchResponse(t, res, MatchV3Counts([]int{len(allRooms), 0}), MatchV3Ops(
		MatchV3DeleteOp(1, 0),
		MatchV3DeleteOp(0, 1),
		MatchV3InsertOp(0, 0, unencryptedRoomID),
	))

	// requesting the encrypted list from scratch returns 2 rooms now
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
			},
			Filters: &sync3.RequestFilters{
				IsEncrypted: &boolTrue,
			},
		}},
	})
	MatchResponse(t, res, MatchV3Count(2), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != len(allRooms) {
				return fmt.Errorf("want %d rooms, got %d", len(allRooms), len(op.Rooms))
			}
			wantRooms := []roomEvents{allRooms[1], allRooms[0]}
			for i := range wantRooms {
				err := wantRooms[i].MatchRoom(
					op.Rooms[i],
				)
				if err != nil {
					return err
				}
			}
			return nil
		}),
	))

	// requesting the unencrypted stream from scratch returns 0 rooms
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
			},
			Filters: &sync3.RequestFilters{
				IsEncrypted: &boolFalse,
			},
		}},
	})
	MatchResponse(t, res, MatchV3Count(0))
}

func TestFiltersInvite(t *testing.T) {
	t.SkipNow()
	boolTrue := true
	boolFalse := false
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Invite: map[string]sync2.SyncV2InviteResponse{
				roomID: {},
			},
		},
	})

	// make sure the is_invite filter works
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolTrue,
				},
			},
			{
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					IsInvite: &boolFalse,
				},
			},
		},
	})
	MatchResponse(t, res, MatchV3Counts([]int{1, 0}), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != 1 {
				return fmt.Errorf("want %d rooms, got %d", 1, len(op.Rooms))
			}
			if op.List != 0 {
				return fmt.Errorf("unknown list: %d", op.List)
			}
			if op.Rooms[0].RoomID != roomID {
				return fmt.Errorf("unknown invite room: %s", op.Rooms[0].RoomID)
			}
			return nil
		}),
	))

	// Accept the invite
	// v2.waitUntilEmpty(t, alice)
	/*
		// now the room should move from one room to another
		res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
			Lists: []sync3.RequestList{
				{
					Ranges: sync3.SliceRanges{
						[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
					},
					// sticky; should remember filters
				},
				{
					Ranges: sync3.SliceRanges{
						[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
					},
					// sticky; should remember filters
				},
			},
		})
		MatchResponse(t, res, MatchV3Counts([]int{len(allRooms), 0}), MatchV3Ops(
			MatchV3DeleteOp(1, 0),
			MatchV3DeleteOp(0, 1),
			MatchV3InsertOp(0, 0, unencryptedRoomID),
		)) */
}
