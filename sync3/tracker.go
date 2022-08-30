package sync3

import (
	"sync"
)

type set map[string]struct{}

// Tracks who is joined to which rooms. This is critical from a security perspective in order to
// ensure that only the users joined to the room receive events in that room. Consider the situation
// where Alice and Bob are joined to room X. If Alice gets kicked from X, the proxy server will still
// receive messages for room X due to Bob being joined to the room. We therefore need to decide which
// active connections should be pushed events, which is what this tracker does.
type JoinedRoomsTracker struct {
	// map of room_id to joined user IDs.
	roomIDToJoinedUsers map[string]set
	userIDToJoinedRooms map[string]set
	// not for security, just to track invite counts correctly as Synapse can send dupe invite->join events
	// so increment +-1 counts don't work.
	roomIDToInvitedUsers map[string]set
	mu                   *sync.RWMutex
}

func NewJoinedRoomsTracker() *JoinedRoomsTracker {
	return &JoinedRoomsTracker{
		roomIDToJoinedUsers:  make(map[string]set),
		userIDToJoinedRooms:  make(map[string]set),
		roomIDToInvitedUsers: make(map[string]set),
		mu:                   &sync.RWMutex{},
	}
}

func (t *JoinedRoomsTracker) IsUserJoined(userID, roomID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	users := t.roomIDToJoinedUsers[roomID]
	for u := range users {
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
	for u := range users {
		if u == userID {
			wasJoined = true
		}
	}
	joinedRooms := t.userIDToJoinedRooms[userID]
	if joinedRooms == nil {
		joinedRooms = make(set)
	}
	joinedUsers := t.roomIDToJoinedUsers[roomID]
	if joinedUsers == nil {
		joinedUsers = make(set)
	}
	invitedUsers := t.roomIDToInvitedUsers[roomID]
	delete(invitedUsers, userID)
	joinedRooms[roomID] = struct{}{}
	joinedUsers[userID] = struct{}{}
	t.userIDToJoinedRooms[userID] = joinedRooms
	t.roomIDToJoinedUsers[roomID] = joinedUsers
	t.roomIDToInvitedUsers[roomID] = invitedUsers
	return !wasJoined
}

func (t *JoinedRoomsTracker) UserLeftRoom(userID, roomID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	joinedRooms := t.userIDToJoinedRooms[userID]
	delete(joinedRooms, roomID)
	joinedUsers := t.roomIDToJoinedUsers[roomID]
	delete(joinedUsers, userID)
	invitedUsers := t.roomIDToInvitedUsers[roomID]
	delete(invitedUsers, userID)
	t.userIDToJoinedRooms[userID] = joinedRooms
	t.roomIDToJoinedUsers[roomID] = joinedUsers
	t.roomIDToInvitedUsers[roomID] = invitedUsers
}
func (t *JoinedRoomsTracker) JoinedRoomsForUser(userID string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	rooms := t.userIDToJoinedRooms[userID]
	if rooms == nil || len(rooms) == 0 {
		return nil
	}
	n := len(rooms)
	i := 0
	result := make([]string, n)
	for roomID := range rooms {
		result[i] = roomID
		i++
	}
	return result
}
func (t *JoinedRoomsTracker) JoinedUsersForRoom(roomID string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	users := t.roomIDToJoinedUsers[roomID]
	if users == nil || len(users) == 0 {
		return nil
	}
	n := len(users)
	i := 0
	result := make([]string, n)
	for userID := range users {
		result[i] = userID
		i++
	}
	return result
}

// Returns the new invite count
func (t *JoinedRoomsTracker) UserInvitedToRoom(userID, roomID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	users := t.roomIDToInvitedUsers[roomID]
	if users == nil {
		users = make(set)
	}
	users[userID] = struct{}{}
	t.roomIDToInvitedUsers[roomID] = users
}

func (t *JoinedRoomsTracker) NumInvitedUsersForRoom(roomID string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.roomIDToInvitedUsers[roomID])
}
