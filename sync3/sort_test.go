package sync3

import (
	"reflect"
	"strings"
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

type finder struct {
	rooms   map[string]*RoomConnMetadata
	roomIDs []string
}

func (f finder) ReadOnlyRoom(roomID string) *RoomConnMetadata {
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
				RoomID: room1,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    3,
				NotificationCount: 12,
				CanonicalisedName: "foo",
			},
			LastInterestedEventTimestamp: 600,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID: room2,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 3,
				CanonicalisedName: "koo",
			},
			LastInterestedEventTimestamp: 700,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID: room3,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    2,
				NotificationCount: 7,
				CanonicalisedName: "yoo",
			},
			LastInterestedEventTimestamp: 900,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID: room4,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 1,
				CanonicalisedName: "boo",
			},
			LastInterestedEventTimestamp: 800,
		},
	}
	// name: 4,1,2,3
	// recency: 3,4,2,1
	// highlight: 1,3,4,2
	// notif: 1,3,2,4
	// level+recency: 3,4,1,2 as 3,4,1 have highlights then sorted by recency
	wantMap := map[string][]string{
		SortByName:              {room4, room1, room2, room3},
		SortByRecency:           {room3, room4, room2, room1},
		SortByHighlightCount:    {room1, room3, room4, room2},
		SortByNotificationCount: {room1, room3, room2, room4},
		SortByNotificationLevel + " " + SortByRecency: {room3, room4, room1, room2},
	}
	f := newFinder(rooms)
	sr := NewSortableRooms(f, f.roomIDs)
	for sortBy, wantOrder := range wantMap {
		sr.Sort(strings.Split(sortBy, " "))
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
				RoomID: room1,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 1,
				CanonicalisedName: "foo",
			},
			LastInterestedEventTimestamp: 600,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID: room2,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 5,
				CanonicalisedName: "koo",
			},
			LastInterestedEventTimestamp: 700,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID: room3,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 0,
				CanonicalisedName: "yoo",
			},
			LastInterestedEventTimestamp: 800,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID: room4,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 0,
				CanonicalisedName: "boo",
			},
			LastInterestedEventTimestamp: 900,
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
		{
			SortBy:    []string{SortByNotificationLevel, SortByName},
			WantRooms: []string{room1, room2, room4, room3},
		},
		{
			SortBy:    []string{SortByNotificationLevel, SortByRecency},
			WantRooms: []string{room2, room1, room4, room3},
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
				RoomID: room1,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 1,
				CanonicalisedName: "foo",
			},
			LastInterestedEventTimestamp: 700,
		},
		{
			RoomMetadata: internal.RoomMetadata{
				RoomID: room2,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    2,
				NotificationCount: 2,
				CanonicalisedName: "foo2",
			},
			LastInterestedEventTimestamp: 600,
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

// dedicated test as it relies on multiple fields
func TestSortByNotificationLevel(t *testing.T) {
	// create the full set of possible sort variables, most recent message last
	roomUnencHC := "!unencrypted-highlight-count:localhost"
	roomUnencHCNC := "!unencrypted-highlight-and-notif-count:localhost"
	roomUnencNC := "!unencrypted-notif-count:localhost"
	roomUnenc := "!unencrypted:localhost"
	roomEncHC := "!encrypted-highlight-count:localhost"
	roomEncHCNC := "!encrypted-highlight-and-notif-count:localhost"
	roomEncNC := "!encrypted-notif-count:localhost"
	roomEnc := "!encrypted:localhost"
	roomsMap := map[string]*RoomConnMetadata{
		roomUnencHC: {
			RoomMetadata: internal.RoomMetadata{
				Encrypted: false,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 0,
			},
			LastInterestedEventTimestamp: 1,
		},
		roomUnencHCNC: {
			RoomMetadata: internal.RoomMetadata{
				Encrypted: false,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 1,
			},
			LastInterestedEventTimestamp: 2,
		},
		roomUnencNC: {
			RoomMetadata: internal.RoomMetadata{
				Encrypted: false,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 1,
			},
			LastInterestedEventTimestamp: 3,
		},
		roomUnenc: {
			RoomMetadata: internal.RoomMetadata{
				Encrypted: false,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 0,
			},
			LastInterestedEventTimestamp: 4,
		},
		roomEncHC: {
			RoomMetadata: internal.RoomMetadata{
				Encrypted: true,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 0,
			},
			LastInterestedEventTimestamp: 5,
		},
		roomEncHCNC: {
			RoomMetadata: internal.RoomMetadata{
				Encrypted: true,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    1,
				NotificationCount: 1,
			},
			LastInterestedEventTimestamp: 6,
		},
		roomEncNC: {
			RoomMetadata: internal.RoomMetadata{
				RoomID:    roomEncNC,
				Encrypted: true,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 1,
			},
			LastInterestedEventTimestamp: 7,
		},
		roomEnc: {
			RoomMetadata: internal.RoomMetadata{
				RoomID:    roomEnc,
				Encrypted: true,
			},
			UserRoomData: caches.UserRoomData{
				HighlightCount:    0,
				NotificationCount: 0,
			},
			LastInterestedEventTimestamp: 8,
		},
	}
	roomIDs := make([]string, len(roomsMap))
	rooms := make([]*RoomConnMetadata, len(roomsMap))
	i := 0
	for roomID, room := range roomsMap {
		room.RoomMetadata.RoomID = roomID
		roomIDs[i] = roomID
		rooms[i] = room
		i++
	}
	t.Logf("%v", roomIDs)
	f := newFinder(rooms)
	sr := NewSortableRooms(f, roomIDs)
	if err := sr.Sort([]string{SortByNotificationLevel, SortByRecency}); err != nil {
		t.Fatalf("Sort: %s", err)
	}
	var gotRoomIDs []string
	for i := range sr.roomIDs {
		gotRoomIDs = append(gotRoomIDs, sr.roomIDs[i])
	}
	// we expect the rooms to be grouped in this order:
	// HIGHLIGHT COUNT > 0
	// ENCRYPTED, NOTIF COUNT > 0
	// UNENCRYPTED, NOTIF COUNT > 0
	// REST
	// Within each group, we expect recency sorting due to SortByRecency
	wantRoomIDs := []string{
		roomEncHCNC, roomEncHC, roomUnencHCNC, roomUnencHC, // in practice we don't expect to see this as encrypted rooms won't have highlight counts > 0
		roomEncNC,
		roomUnencNC,
		roomEnc, roomUnenc,
	}

	if !reflect.DeepEqual(gotRoomIDs, wantRoomIDs) {
		t.Errorf("got: %v", gotRoomIDs)
		t.Errorf("want: %v", wantRoomIDs)
	}
}
