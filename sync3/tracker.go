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

// Startup efficiently sets up the joined rooms tracker, but isn't safe to call with live traffic,
// as it replaces all known in-memory state. Panics if called on a non-empty tracker.
func (t *JoinedRoomsTracker) Startup(roomToJoinedUsers map[string][]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.roomIDToJoinedUsers) > 0 || len(t.userIDToJoinedRooms) > 0 {
		panic("programming error: cannot call JoinedRoomsTracker.Startup with existing data already set!")
	}
	for roomID, userIDs := range roomToJoinedUsers {
		userSet := make(set)
		for _, u := range userIDs {
			userSet[u] = struct{}{}
			rooms := t.userIDToJoinedRooms[u]
			if rooms == nil {
				rooms = make(set)
			}
			rooms[roomID] = struct{}{}
			t.userIDToJoinedRooms[u] = rooms
		}
		t.roomIDToJoinedUsers[roomID] = userSet
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

// UserJoinedRoom marks the given user as having joined the given room. Returns true
// if the user was not joined to the room prior to this call, and false otherwise.
func (t *JoinedRoomsTracker) UserJoinedRoom(userID, roomID string) bool {
	u := make([]string, 1, 1)
	u[0] = userID
	return t.UsersJoinedRoom(u, roomID)
}

// UsersJoinedRoom marks the given slice of users as having joined the given room.
// Returns true if at least one of the users was not joined to the room prior to the
// call, and false otherwise.
func (t *JoinedRoomsTracker) UsersJoinedRoom(userIDs []string, roomID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasJoined := true
	users := t.roomIDToJoinedUsers[roomID]
	for _, newlyJoinedUser := range userIDs {
		_, exists := users[newlyJoinedUser]
		if !exists {
			wasJoined = false
			break
		}
	}

	// pull out room specific structs
	joinedUsers := t.roomIDToJoinedUsers[roomID]
	if joinedUsers == nil {
		joinedUsers = make(set)
	}
	invitedUsers := t.roomIDToInvitedUsers[roomID]

	// loop user specific structs
	for _, newlyJoinedUser := range userIDs {
		joinedRooms := t.userIDToJoinedRooms[newlyJoinedUser]
		if joinedRooms == nil {
			joinedRooms = make(set)
		}

		delete(invitedUsers, newlyJoinedUser)
		joinedRooms[roomID] = struct{}{}
		joinedUsers[newlyJoinedUser] = struct{}{}
		t.userIDToJoinedRooms[newlyJoinedUser] = joinedRooms
	}

	t.roomIDToJoinedUsers[roomID] = joinedUsers
	t.roomIDToInvitedUsers[roomID] = invitedUsers
	return !wasJoined
}

// UserLeftRoom marks the given user as having left the given room.
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

// JoinedUsersForRoom returns the joined users in the given room, filtered by the filter function if provided. If one is not
// provided, all joined users are returned. Returns the join count at the time this function was called.
func (t *JoinedRoomsTracker) JoinedUsersForRoom(roomID string, filter func(userID string) bool) (matchedUserIDs []string, joinCount int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	users := t.roomIDToJoinedUsers[roomID]
	if users == nil || len(users) == 0 {
		return nil, 0
	}
	n := len(users)
	if filter == nil {
		filter = func(userID string) bool { return true }
	}
	for userID := range users {
		if filter(userID) {
			matchedUserIDs = append(matchedUserIDs, userID)
		}
	}
	return matchedUserIDs, n
}

func (t *JoinedRoomsTracker) UsersInvitedToRoom(userIDs []string, roomID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	users := t.roomIDToInvitedUsers[roomID]
	if users == nil {
		users = make(set)
	}
	for _, userID := range userIDs {
		users[userID] = struct{}{}
	}
	t.roomIDToInvitedUsers[roomID] = users
}

func (t *JoinedRoomsTracker) NumInvitedUsersForRoom(roomID string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.roomIDToInvitedUsers[roomID])
}
