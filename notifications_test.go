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

// Test that sort operations that favour notif counts always appear at the start of the list.
func TestNotificationsOnTop(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	bob := "@TestNotificationsOnTop_bob:localhost"
	bingRoomID := "!TestNotificationsOnTop_bing:localhost"
	noBingRoomID := "!TestNotificationsOnTop_nobing:localhost"
	latestTimestamp := time.Now()
	allRooms := []roomEvents{
		// this room on top when sorted by recency
		{
			roomID: noBingRoomID,
			events: append(createRoomState(t, alice, latestTimestamp), []json.RawMessage{
				testutils.NewStateEvent(
					t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join", "displayname": "Bob"},
					testutils.WithTimestamp(latestTimestamp.Add(5*time.Second)),
				),
			}...),
		},
		{
			roomID: bingRoomID,
			events: append(createRoomState(t, alice, latestTimestamp), []json.RawMessage{
				testutils.NewStateEvent(
					t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join", "displayname": "Bob"},
					testutils.WithTimestamp(latestTimestamp),
				),
			}...),
		},
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// connect and make sure we get nobing, bing
	syncRequestBody := sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: int64(100),
			},
			// prefer highlight count first, THEN eventually recency
			Sort: []string{sync3.SortByHighlightCount, sync3.SortByNotificationCount, sync3.SortByRecency},
		}},
	}
	res := v3.mustDoV3Request(t, aliceToken, syncRequestBody)
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != len(allRooms) {
				return fmt.Errorf("want %d rooms, got %d", len(allRooms), len(op.Rooms))
			}
			for i := range allRooms {
				err := allRooms[i].MatchRoom(
					op.Rooms[i],
				)
				if err != nil {
					return err
				}
			}
			return nil
		}),
	))

	// send a bing message into the bing room, make sure it comes through and is on top
	bingEvent := testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{"body": "BING!"}, testutils.WithTimestamp(latestTimestamp.Add(1*time.Minute)))
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				bingRoomID: {
					UnreadNotifications: sync2.UnreadNotifications{
						HighlightCount: ptr(1),
					},
					Timeline: sync2.TimelineResponse{
						Events: []json.RawMessage{
							bingEvent,
						},
					},
				},
			},
		},
	})
	v2.waitUntilEmpty(t, alice)
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, syncRequestBody)
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3DeleteOp(0, 1),
		MatchV3InsertOp(0, 0, bingRoomID),
	))

	// send a message into the nobing room, it's position must not change due to our sort order
	noBingEvent := testutils.NewEvent(t, "m.room.message", bob, map[string]interface{}{"body": "no bing"}, testutils.WithTimestamp(latestTimestamp.Add(2*time.Minute)))
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				noBingRoomID: {
					Timeline: sync2.TimelineResponse{
						Events: []json.RawMessage{
							noBingEvent,
						},
					},
				},
			},
		},
	})
	v2.waitUntilEmpty(t, alice)
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, syncRequestBody)
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3UpdateOp(0, 1, noBingRoomID),
	))

	// restart the server and sync from fresh again, it should still have the bing room on top
	v3.restart(t, v2, pqString)
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: int64(100),
			},
			// prefer highlight count first, THEN eventually recency
			Sort: []string{sync3.SortByHighlightCount, sync3.SortByNotificationCount, sync3.SortByRecency},
		}},
	})
	MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
		MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
			if len(op.Rooms) != len(allRooms) {
				return fmt.Errorf("want %d rooms, got %d", len(allRooms), len(op.Rooms))
			}
			err := allRooms[1].MatchRoom(
				op.Rooms[0], // bing room is first
				MatchRoomHighlightCount(1),
				MatchRoomNotificationCount(0),
				MatchRoomTimelineMostRecent(1, []json.RawMessage{bingEvent}),
			)
			if err != nil {
				return err
			}
			err = allRooms[0].MatchRoom(
				op.Rooms[1], // no bing room is second
				MatchRoomHighlightCount(0),
				MatchRoomNotificationCount(0),
				MatchRoomTimelineMostRecent(1, []json.RawMessage{noBingEvent}),
			)
			if err != nil {
				return err
			}
			return nil
		}),
	))
}
