package state

import (
	"encoding/json"
	"testing"

	"github.com/matrix-org/sync-v3/testutils"
)

func TestStorageJoinedRoomsAfterPosition(t *testing.T) {
	store := NewStorage(postgresConnectionString)
	joinedRoomID := "!joined:bar"
	invitedRoomID := "!invited:bar"
	leftRoomID := "!left:bar"
	banRoomID := "!ban:bar"
	bobJoinedRoomID := "!bobjoined:bar"
	alice := "@aliceTestStorageJoinedRoomsAfterPosition:localhost"
	bob := "@bobTestStorageJoinedRoomsAfterPosition:localhost"
	roomIDToEventMap := map[string][]json.RawMessage{
		joinedRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
			testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
		},
		invitedRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
			testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{"membership": "invite"}),
		},
		leftRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
			testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
			testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "leave"}),
		},
		banRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
			testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
			testutils.NewStateEvent(t, "m.room.member", alice, bob, map[string]interface{}{"membership": "ban"}),
		},
		bobJoinedRoomID: {
			testutils.NewStateEvent(t, "m.room.create", "", bob, map[string]interface{}{"creator": bob}),
			testutils.NewStateEvent(t, "m.room.member", bob, bob, map[string]interface{}{"membership": "join"}),
		},
	}
	for roomID, eventMap := range roomIDToEventMap {
		store.Accumulate(roomID, eventMap)
	}
	latestPos, err := store.LatestEventNID()
	if err != nil {
		t.Fatalf("failed to get latest event nid: %s", err)
	}
	aliceJoinedRooms, err := store.JoinedRoomsAfterPosition(alice, latestPos)
	if err != nil {
		t.Fatalf("failed to JoinedRoomsAfterPosition: %s", err)
	}
	if len(aliceJoinedRooms) != 1 || aliceJoinedRooms[0] != joinedRoomID {
		t.Fatalf("JoinedRoomsAfterPosition at %v for %s got %v want %v", latestPos, alice, aliceJoinedRooms, joinedRoomID)
	}
	bobJoinedRooms, err := store.JoinedRoomsAfterPosition(bob, latestPos)
	if err != nil {
		t.Fatalf("failed to JoinedRoomsAfterPosition: %s", err)
	}
	if len(bobJoinedRooms) != 3 {
		t.Fatalf("JoinedRoomsAfterPosition for %s got %v rooms want %v", bob, len(bobJoinedRooms), 3)
	}
}
