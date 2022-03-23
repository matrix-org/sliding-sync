package sync3

import (
	"testing"

	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sync3/caches"
)

func TestSortBySingleOperation(t *testing.T) {
	room1 := "!1:localhost"
	room2 := "!2:localhost"
	room3 := "!3:localhost"
	room4 := "!4:localhost"
	rooms := []RoomConnMetadata{
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room1,
				LastMessageTimestamp: 600,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    3,
				NotificationCount: 12,
			},
			CanonicalisedName: "foo",
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room2,
				LastMessageTimestamp: 700,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 3,
			},
			CanonicalisedName: "koo",
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room3,
				LastMessageTimestamp: 900,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    2,
				NotificationCount: 7,
			},
			CanonicalisedName: "yoo",
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room4,
				LastMessageTimestamp: 800,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 1,
			},
			CanonicalisedName: "boo",
		},
	}
	// name: 4,1,2,3
	// recency: 3,4,2,1
	// highlight: 1,3,4,2
	// notif: 1,3,2,4
	wantMap := map[string][]string{
		SortByName:              {room4, room1, room2, room3},
		SortByRecency:           {room3, room4, room2, room1},
		SortByHighlightCount:    {room1, room3, room4, room2},
		SortByNotificationCount: {room1, room3, room2, room4},
	}
	sr := NewSortableRooms(rooms)
	for sortBy, wantOrder := range wantMap {
		sr.Sort([]string{sortBy})
		var gotRoomIDs []string
		for i := range sr.rooms {
			gotRoomIDs = append(gotRoomIDs, sr.rooms[i].RoomID)
		}
		for i := range wantOrder {
			if wantOrder[i] != gotRoomIDs[i] {
				t.Errorf("Sort: %s got %v want %v", sortBy, gotRoomIDs, wantOrder)
			}
		}
	}
}

func TestSortByMultipleOperations(t *testing.T) {
	room1 := "!1:localhost"
	room2 := "!2:localhost"
	room3 := "!3:localhost"
	room4 := "!4:localhost"
	rooms := []RoomConnMetadata{
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room1,
				LastMessageTimestamp: 600,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 1,
			},
			CanonicalisedName: "foo",
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room2,
				LastMessageTimestamp: 700,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 5,
			},
			CanonicalisedName: "koo",
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room3,
				LastMessageTimestamp: 800,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 0,
			},
			CanonicalisedName: "yoo",
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room4,
				LastMessageTimestamp: 900,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 0,
			},
			CanonicalisedName: "boo",
		},
	}
	testCases := []struct {
		SortBy    []string
		WantRooms []string
	}{
		{
			SortBy:    []string{SortByHighlightCount, SortByNotificationCount, SortByRecency, SortByName},
			WantRooms: []string{room2, room1, room4, room3},
		},
		{
			SortBy:    []string{SortByHighlightCount, SortByName},
			WantRooms: []string{room1, room2, room4, room3},
		},
	}
	sr := NewSortableRooms(rooms)
	for _, tc := range testCases {
		sr.Sort(tc.SortBy)
		var gotRoomIDs []string
		for i := range sr.rooms {
			gotRoomIDs = append(gotRoomIDs, sr.rooms[i].RoomID)
		}
		for i := range tc.WantRooms {
			if tc.WantRooms[i] != gotRoomIDs[i] {
				t.Errorf("Sort: %v got %v want %v", tc.SortBy, gotRoomIDs, tc.WantRooms)
			}
		}
	}
}
