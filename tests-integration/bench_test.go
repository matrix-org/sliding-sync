package syncv3

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
	"github.com/rs/zerolog"
)

// The purpose of this benchmark is to ensure that sync v3 responds quickly regardless of how many
// rooms the user has on their account. The first initial sync to fetch the rooms are ignored for
// the purposes of the benchmark, only subsequent requests are taken into account. We expect this
// value to grow slowly (searching more rows in a database, looping more elements in an array) but
// this should not grow so quickly that it impacts performance or else something has gone terribly
// wrong.
func BenchmarkNumRooms(b *testing.B) {
	testutils.Quiet = true
	for _, numRooms := range []int{100, 200, 400, 800} {
		n := numRooms
		b.Run(fmt.Sprintf("num_rooms_%d", n), func(b *testing.B) {
			benchNumV2Rooms(n, b)
		})
	}
}

func benchNumV2Rooms(numRooms int, b *testing.B) {
	// setup code
	boolFalse := false
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(b)
	v3 := runTestServer(b, v2, pqString)
	zerolog.SetGlobalLevel(zerolog.Disabled)
	defer v2.close()
	defer v3.close()
	allRooms := make([]roomEvents, numRooms)
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(time.Duration(i) * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!benchNumV2Rooms_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(b, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(b, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(b, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(b, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(b, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
			}...),
		}
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})
	// do the initial request
	v3.mustDoV3Request(b, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 20}, // first few rooms
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 3,
			},
		}},
	})

	b.ResetTimer() // don't count setup code

	// these should all take roughly the same amount of time, regardless of the value of `numRooms`
	for n := 0; n < b.N; n++ {
		v3.mustDoV3Request(b, aliceToken, sync3.Request{
			Lists: []sync3.RequestList{{
				// always use a fixed range else we will scale O(n) with the number of rooms
				Ranges: sync3.SliceRanges{
					[2]int64{0, 20}, // first few rooms
				},
				// include a filter to ensure we loop over rooms
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolFalse,
				},
				// include a few required state events to force us to query the database
				// include a few timeline events to force us to query the database
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 3,
					RequiredState: [][2]string{
						{"m.room.create", ""},
						{"m.room.member", alice},
					},
				},
			}},
		})
	}
}
