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

// Test that room names come through sanely. Additional testing to ensure we copy hero slices correctly.
func TestRoomNames(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	bob := "@TestRoomNames_bob:localhost"
	// make 5 rooms, last room is most recent, and send A,B,C into each room
	latestTimestamp := time.Now()
	allRooms := []roomEvents{
		{
			roomID: "!TestRoomNames_dm:localhost",
			name:   "Bob",
			events: append(createRoomState(t, alice, latestTimestamp), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join", "displayname": "Bob"}, testutils.WithTimestamp(latestTimestamp.Add(3*time.Second))),
			}...),
		},
		{
			roomID: "!TestRoomNames_named:localhost",
			name:   "My Room Name",
			events: append(createRoomState(t, alice, latestTimestamp), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": "My Room Name"}, testutils.WithTimestamp(latestTimestamp.Add(2*time.Second))),
			}...),
		},
		{
			roomID: "!TestRoomNames_empty:localhost",
			name:   "Empty Room",
			events: createRoomState(t, alice, latestTimestamp.Add(time.Second)),
		},
		{
			roomID: "!TestRoomNames_dm_name_set_after_join:localhost",
			name:   "Bob",
			state: append(createRoomState(t, alice, latestTimestamp), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}, testutils.WithTimestamp(latestTimestamp)),
			}...),
			events: []json.RawMessage{
				testutils.NewStateEvent(
					t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join", "displayname": "Bob"}, testutils.WithTimestamp(latestTimestamp),
					testutils.WithUnsigned(map[string]interface{}{
						"prev_content": map[string]interface{}{
							"membership": "join",
						},
					})),
			},
		},
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	checkRoomNames := func(sessionID string) {
		t.Helper()
		// do a sync, make sure room names are sensible
		res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
			Lists: []sync3.RequestList{{
				Ranges: sync3.SliceRanges{
					[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: int64(100),
				},
			}},
		})
		MatchResponse(t, res, MatchV3Count(len(allRooms)), MatchV3Ops(
			MatchV3SyncOp(func(op *sync3.ResponseOpRange) error {
				if len(op.Rooms) != len(allRooms) {
					return fmt.Errorf("want %d rooms, got %d", len(allRooms), len(op.Rooms))
				}
				for i := range allRooms {
					err := allRooms[i].MatchRoom(
						op.Rooms[i],
						MatchRoomName(allRooms[i].name),
					)
					if err != nil {
						return err
					}
				}
				return nil
			}),
		))
	}

	checkRoomNames("a")
	// restart the server and repeat the tests, should still be the same when reading from the database
	v3.restart(t, v2, pqString)
	checkRoomNames("b")

	// now check that we can filter the rooms by name
	checkRoomNameFilter := func(searchTerm string, wantRooms []roomEvents) {
		t.Helper()
		// do a sync, make sure room names are sensible
		res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
			Lists: []sync3.RequestList{{
				Ranges: sync3.SliceRanges{
					[2]int64{0, int64(len(allRooms) - 1)}, // all rooms
				},
				Filters: &sync3.RequestFilters{
					RoomNameFilter: searchTerm,
				},
			}},
		})
		matchers := make([][]roomMatcher, len(wantRooms))
		for i := range wantRooms {
			matchers[i] = []roomMatcher{
				MatchRoomName(wantRooms[i].name),
				MatchRoomID(wantRooms[i].roomID),
			}
		}
		MatchResponse(t, res, MatchV3Count(len(wantRooms)), MatchV3Ops(
			MatchV3SyncOpWithMatchers(MatchRoomRange(matchers...)),
		))
	}
	// case-insensitive matching
	checkRoomNameFilter("my room name", []roomEvents{allRooms[1]})
	// partial matching
	checkRoomNameFilter("room na", []roomEvents{allRooms[1]})
	// multiple matches
	checkRoomNameFilter("bob", []roomEvents{allRooms[0], allRooms[3]})
}
