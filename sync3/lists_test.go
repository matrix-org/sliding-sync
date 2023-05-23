package sync3_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

var roomCounter atomic.Int64
var timestamp = time.Now()

func BenchmarkSortRooms(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sortRooms(4000)
	}
}

func sortRooms(n int) {
	list := sync3.NewInternalRequestLists()
	addRooms(list, n)
	list.AssignList(context.Background(), "benchmark", &sync3.RequestFilters{}, []string{sync3.SortByRecency}, sync3.Overwrite)
}

func addRooms(list *sync3.InternalRequestLists, n int) {
	for i := 0; i < n; i++ {
		next := roomCounter.Add(1)
		messageTimestamp := uint64(timestamp.Add(time.Duration(next) * time.Minute).UnixMilli())
		list.SetRoom(sync3.RoomConnMetadata{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               fmt.Sprintf("!%d:benchmark", next),
				JoinCount:            int(next % 100),
				NameEvent:            fmt.Sprintf("Room %d", next),
				LastMessageTimestamp: messageTimestamp,
			},
			UserRoomData: caches.UserRoomData{
				IsDM: next%10 == 0,
			},
			LastActivityTimestamp: messageTimestamp,
		}, true)
	}
}
