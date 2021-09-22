package synclive

import (
	"sync"
)

type JoinedRoomsTracker struct {
	// map of room_id to joined user IDs.
	roomIDToJoinedUsers map[string][]string
	userIDToJoinedRooms map[string][]string
	mu                  *sync.RWMutex
}

func NewJoinedRoomsTracker() *JoinedRoomsTracker {
	return &JoinedRoomsTracker{
		roomIDToJoinedUsers: make(map[string][]string),
		userIDToJoinedRooms: make(map[string][]string),
		mu:                  &sync.RWMutex{},
	}
}

func (t *JoinedRoomsTracker) UserJoinedRoom(userID, roomID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	joinedRooms := t.userIDToJoinedRooms[userID]
	joinedUsers := t.roomIDToJoinedUsers[roomID]
	joinedRooms = append(joinedRooms, roomID)
	joinedUsers = append(joinedUsers, userID)
	t.userIDToJoinedRooms[userID] = joinedRooms
	t.roomIDToJoinedUsers[roomID] = joinedUsers
}

func (t *JoinedRoomsTracker) UserLeftRoom(userID, roomID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	joinedRooms := t.userIDToJoinedRooms[userID]
	for i := 0; i < len(joinedRooms); i++ {
		if roomID == joinedRooms[i] {
			// remove without preserving order
			joinedRooms[i] = joinedRooms[len(joinedRooms)-1]
			joinedRooms = joinedRooms[:len(joinedRooms)-1]
		}
	}
	joinedUsers := t.roomIDToJoinedUsers[roomID]
	for i := 0; i < len(joinedUsers); i++ {
		if userID == joinedUsers[i] {
			// remove without preserving order
			joinedUsers[i] = joinedUsers[len(joinedUsers)-1]
			joinedUsers = joinedUsers[:len(joinedUsers)-1]
		}
	}
	t.userIDToJoinedRooms[userID] = joinedRooms
	t.roomIDToJoinedUsers[roomID] = joinedUsers
}
func (t *JoinedRoomsTracker) JoinedRoomsForUser(userID string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.userIDToJoinedRooms[userID]
}
func (t *JoinedRoomsTracker) JoinedUsersForRoom(roomID string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.roomIDToJoinedUsers[roomID]
}
