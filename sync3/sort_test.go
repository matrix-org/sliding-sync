package sync3

import (
	"testing"

	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sync3/caches"
)

type finder struct {
	rooms   map[string]*RoomConnMetadata
	roomIDs []string
}

func (f finder) Room(roomID string) *RoomConnMetadata {
	return f.rooms[roomID]
}

func newFinder(rooms []*RoomConnMetadata) finder {
	m := make(map[string]*RoomConnMetadata)
	ids := make([]string, len(rooms))
	for i := range rooms {
		ids[i] = rooms[i].RoomID
		m[ids[i]] = rooms[i]
	}
	return finder{
		rooms:   m,
		roomIDs: ids,
	}
}

func TestSortBySingleOperation(t *testing.T) {
	room1 := "!1:localhost"
	room2 := "!2:localhost"
	room3 := "!3:localhost"
	room4 := "!4:localhost"
	rooms := []*RoomConnMetadata{
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room1,
				LastMessageTimestamp: 600,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    3,
				NotificationCount: 12,
				CanonicalisedName: "foo",
			},
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room2,
				LastMessageTimestamp: 700,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 3,
				CanonicalisedName: "koo",
			},
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room3,
				LastMessageTimestamp: 900,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    2,
				NotificationCount: 7,
				CanonicalisedName: "yoo",
			},
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room4,
				LastMessageTimestamp: 800,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 1,
				CanonicalisedName: "boo",
			},
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
	f := newFinder(rooms)
	sr := NewSortableRooms(f, f.roomIDs)
	for sortBy, wantOrder := range wantMap {
		sr.Sort([]string{sortBy})
		var gotRoomIDs []string
		for i := range sr.roomIDs {
			gotRoomIDs = append(gotRoomIDs, sr.roomIDs[i])
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
	rooms := []*RoomConnMetadata{
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room1,
				LastMessageTimestamp: 600,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 1,
				CanonicalisedName: "foo",
			},
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room2,
				LastMessageTimestamp: 700,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 5,
				CanonicalisedName: "koo",
			},
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room3,
				LastMessageTimestamp: 800,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 0,
				CanonicalisedName: "yoo",
			},
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room4,
				LastMessageTimestamp: 900,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 0,
				CanonicalisedName: "boo",
			},
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
	f := newFinder(rooms)
	sr := NewSortableRooms(f, f.roomIDs)
	for _, tc := range testCases {
		sr.Sort(tc.SortBy)
		var gotRoomIDs []string
		for i := range sr.roomIDs {
			gotRoomIDs = append(gotRoomIDs, sr.roomIDs[i])
		}
		for i := range tc.WantRooms {
			if tc.WantRooms[i] != gotRoomIDs[i] {
				t.Errorf("Sort: %v got %v want %v", tc.SortBy, gotRoomIDs, tc.WantRooms)
			}
		}
	}
}

// Test that if you remove a room, it updates the lookup map.
func TestSortableRoomsRemove(t *testing.T) {
	room1 := "!1:localhost"
	room2 := "!2:localhost"
	rooms := []*RoomConnMetadata{
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room1,
				LastMessageTimestamp: 700,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 1,
				CanonicalisedName: "foo",
			},
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID:               room2,
				LastMessageTimestamp: 600,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    2,
				NotificationCount: 2,
				CanonicalisedName: "foo2",
			},
		},
	}
	f := newFinder(rooms)
	sr := NewSortableRooms(f, f.roomIDs)
	if err := sr.Sort([]string{SortByRecency}); err != nil { // room 1 is first, then room 2
		t.Fatalf("Sort: %s", err)
	}
	if i, ok := sr.IndexOf(room1); i != 0 || !ok {
		t.Errorf("IndexOf room 1 returned %v %v", i, ok)
	}
	if i, ok := sr.IndexOf(room2); i != 1 || !ok {
		t.Errorf("IndexOf room 2 returned %v %v", i, ok)
	}
	// Remove room 1, so room 2 should take its place.
	rmIndex := sr.Remove(room1)
	if rmIndex != 0 {
		t.Fatalf("Remove: return removed index %v want 0", rmIndex)
	}
	// check
	if i, ok := sr.IndexOf(room1); ok { // should be !ok
		t.Errorf("IndexOf room 1 returned %v %v", i, ok)
	}
	if i, ok := sr.IndexOf(room2); i != 0 || !ok {
		t.Errorf("IndexOf room 2 returned %v %v", i, ok)
	}
}
