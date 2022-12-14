package handler

type LazyCache struct {
	cache map[string]struct{}
	rooms map[string]struct{}
}

func NewLazyCache() *LazyCache {
	return &LazyCache{
		cache: make(map[string]struct{}),
		rooms: make(map[string]struct{}),
	}
}

func (lc *LazyCache) IsSet(roomID, userID string) bool {
	key := roomID + " | " + userID
	_, exists := lc.cache[key]
	return exists
}

// IsLazyLoading returns true if this room is being lazy loaded.
func (lc *LazyCache) IsLazyLoading(roomID string) bool {
	_, exists := lc.rooms[roomID]
	return exists
}

func (lc *LazyCache) Add(roomID string, userIDs ...string) {
	for _, u := range userIDs {
		lc.AddUser(roomID, u)
	}
}

// AddUser to this room. Returns true if this is the first time this user has done so, and
// hence you should include the member event for this user.
func (lc *LazyCache) AddUser(roomID, userID string) bool {
	lc.rooms[roomID] = struct{}{}
	key := roomID + " | " + userID
	_, exists := lc.cache[key]
	if exists {
		return false
	}
	lc.cache[key] = struct{}{}
	return true
}
