package sync3

import (
	"sync"
)

// Tracks who is joined to which rooms. This is critical from a security perspective in order to
// ensure that only the users joined to the room receive events in that room. Consider the situation
// where Alice and Bob are joined to room X. If Alice gets kicked from X, the proxy server will still
// receive messages for room X due to Bob being joined to the room. We therefore need to decide which
// active connections should be pushed events, which is what this tracker does.
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

func (t *JoinedRoomsTracker) IsUserJoined(userID, roomID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	users := t.roomIDToJoinedUsers[roomID]
	for _, u := range users {
		if u == userID {
			return true
		}
	}
	return false
}

// returns true if the state changed
func (t *JoinedRoomsTracker) UserJoinedRoom(userID, roomID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasJoined := false
	users := t.roomIDToJoinedUsers[roomID]
	for _, u := range users {
		if u == userID {
			wasJoined = true
		}
	}
	joinedRooms := t.userIDToJoinedRooms[userID]
	joinedUsers := t.roomIDToJoinedUsers[roomID]
	joinedRooms = append(joinedRooms, roomID)
	joinedUsers = append(joinedUsers, userID)
	t.userIDToJoinedRooms[userID] = joinedRooms
	t.roomIDToJoinedUsers[roomID] = joinedUsers
	return !wasJoined
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
	rooms := t.userIDToJoinedRooms[userID]
	if len(rooms) == 0 {
		return nil
	}
	roomsCopy := make([]string, len(rooms))
	copy(roomsCopy, rooms)
	return roomsCopy
}
func (t *JoinedRoomsTracker) JoinedUsersForRoom(roomID string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	users := t.roomIDToJoinedUsers[roomID]
	if len(users) == 0 {
		return nil
	}
	usersCopy := make([]string, len(users))
	copy(usersCopy, users)
	return usersCopy
}
