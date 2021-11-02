package sync3

import (
	"testing"

	"github.com/matrix-org/sync-v3/internal"
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
			CanonicalisedName: "foo",
			HighlightCount:    3,
			NotificationCount: 12,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room2,
				LastMessageTimestamp: 700,
			},
			CanonicalisedName: "koo",
			HighlightCount:    0,
			NotificationCount: 3,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room3,
				LastMessageTimestamp: 900,
			},
			CanonicalisedName: "yoo",
			HighlightCount:    2,
			NotificationCount: 7,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room4,
				LastMessageTimestamp: 800,
			},
			CanonicalisedName: "boo",
			HighlightCount:    1,
			NotificationCount: 1,
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
	sr := SortableRooms(rooms)
	for sortBy, wantOrder := range wantMap {
		sr.Sort([]string{sortBy})
		var gotRoomIDs []string
		for i := range sr {
			gotRoomIDs = append(gotRoomIDs, sr[i].RoomID)
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
			CanonicalisedName: "foo",
			HighlightCount:    1,
			NotificationCount: 1,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room2,
				LastMessageTimestamp: 700,
			},
			CanonicalisedName: "koo",
			HighlightCount:    1,
			NotificationCount: 5,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room3,
				LastMessageTimestamp: 800,
			},
			CanonicalisedName: "yoo",
			HighlightCount:    0,
			NotificationCount: 0,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room4,
				LastMessageTimestamp: 900,
			},
			CanonicalisedName: "boo",
			HighlightCount:    0,
			NotificationCount: 0,
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
	sr := SortableRooms(rooms)
	for _, tc := range testCases {
		sr.Sort(tc.SortBy)
		var gotRoomIDs []string
		for i := range sr {
			gotRoomIDs = append(gotRoomIDs, sr[i].RoomID)
		}
		for i := range tc.WantRooms {
			if tc.WantRooms[i] != gotRoomIDs[i] {
				t.Errorf("Sort: %v got %v want %v", tc.SortBy, gotRoomIDs, tc.WantRooms)
			}
		}
	}
}
