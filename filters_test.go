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
func TestFilters(t *testing.T) {
	boolTrue := true
	boolFalse := false
	pqString := testutils.PrepareDBConnectionString(postgresTestDatabaseName)
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	alice := "@TestFilters_alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN_TestFilters"
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
	encryptedSessionID := "encrypted_session"
	encryptedRes := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Rooms: sync3.SliceRanges{
				[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
			},
			Filters: &sync3.RequestFilters{
				IsEncrypted: &boolTrue,
			},
		}},
		SessionID: encryptedSessionID,
	})
	MatchResponse(t, encryptedRes, MatchV3Count(1), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != 1 {
				return fmt.Errorf("want %d rooms, got %d", 1, len(op.Rooms))
			}
			return allRooms[0].MatchRoom(op.Rooms[0]) // encrypted room
		}),
	))

	unencryptedSessionID := "unencrypted_session"
	unencryptedRes := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Rooms: sync3.SliceRanges{
				[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
			},
			Filters: &sync3.RequestFilters{
				IsEncrypted: &boolFalse,
			},
		}},
		SessionID: unencryptedSessionID,
	})
	MatchResponse(t, unencryptedRes, MatchV3Count(1), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != 1 {
				return fmt.Errorf("want %d rooms, got %d", 1, len(op.Rooms))
			}
			return allRooms[1].MatchRoom(op.Rooms[0]) // unencrypted room
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
	encryptedRes = v3.mustDoV3RequestWithPos(t, aliceToken, encryptedRes.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Rooms: sync3.SliceRanges{
				[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
			},
			// sticky; should remember filters
		}},
		SessionID: encryptedSessionID,
	})
	MatchResponse(t, encryptedRes, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3DeleteOp(1),
		MatchV3InsertOp(0, unencryptedRoomID),
	))

	// requesting the encrypted list from scratch returns 2 rooms now
	encryptedRes = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Rooms: sync3.SliceRanges{
				[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
			},
			Filters: &sync3.RequestFilters{
				IsEncrypted: &boolTrue,
			},
		}},
		SessionID: "new_encrypted_session",
	})
	MatchResponse(t, encryptedRes, MatchV3Count(2), MatchV3Ops(
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

	// TODO: requesting the unencrypted stream DELETEs the room without a corresponding INSERT

	// requesting the unencrypted stream from scratch returns 0 rooms
	unencryptedRes = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Rooms: sync3.SliceRanges{
				[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
			},
			Filters: &sync3.RequestFilters{
				IsEncrypted: &boolFalse,
			},
		}},
		SessionID: "new_unencrypted_session",
	})
	MatchResponse(t, unencryptedRes, MatchV3Count(0))
}
