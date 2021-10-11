package sync3

import (
	"sync"
)

type UserRoomData struct {
	NotificationCount int
	HighlightCount    int
}

type UserCacheListener interface {
	OnUnreadCountsChanged(userID, roomID string, urd UserRoomData, hasCountDecreased bool)
}

type UserCache struct {
	userID       string
	roomToData   map[string]UserRoomData
	roomToDataMu *sync.RWMutex
	listeners    map[int]UserCacheListener
	listenersMu  *sync.Mutex
	id           int
}

func NewUserCache(userID string) *UserCache {
	return &UserCache{
		userID:       userID,
		roomToDataMu: &sync.RWMutex{},
		roomToData:   make(map[string]UserRoomData),
		listeners:    make(map[int]UserCacheListener),
		listenersMu:  &sync.Mutex{},
	}
}

func (c *UserCache) Subsribe(ucl UserCacheListener) (id int) {
	c.listenersMu.Lock()
	defer c.listenersMu.Unlock()
	id = c.id
	c.id += 1
	c.listeners[id] = ucl
	return
}

func (c *UserCache) Unsubscribe(id int) {
	c.listenersMu.Lock()
	defer c.listenersMu.Unlock()
	delete(c.listeners, id)
}

func (c *UserCache) loadRoomData(roomID string) UserRoomData {
	c.roomToDataMu.RLock()
	defer c.roomToDataMu.RUnlock()
	data, ok := c.roomToData[roomID]
	if !ok {
		return UserRoomData{}
	}
	return data
}

// =================================================
// Listener functions called by v2 pollers are below
// =================================================

func (c *UserCache) OnUnreadCounts(roomID string, highlightCount, notifCount *int) {
	data := c.loadRoomData(roomID)
	hasCountDecreased := false
	if highlightCount != nil {
		hasCountDecreased = *highlightCount < data.HighlightCount
		data.HighlightCount = *highlightCount
	}
	if notifCount != nil {
		if !hasCountDecreased {
			hasCountDecreased = *notifCount < data.NotificationCount
		}
		data.NotificationCount = *notifCount
	}
	c.roomToDataMu.Lock()
	c.roomToData[roomID] = data
	c.roomToDataMu.Unlock()
	for _, l := range c.listeners {
		l.OnUnreadCountsChanged(c.userID, roomID, data, hasCountDecreased)
	}
}
