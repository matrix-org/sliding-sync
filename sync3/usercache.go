package sync3

import (
	"encoding/json"
	"sync"

	"github.com/matrix-org/sync-v3/state"
)

type UserRoomData struct {
	NotificationCount int
	HighlightCount    int
	Timeline          []json.RawMessage
}

type UserCacheListener interface {
	OnNewEvent(event *EventData)
	OnUnreadCountsChanged(userID, roomID string, urd UserRoomData, hasCountDecreased bool)
}

type UserCache struct {
	LazyRoomDataOverride func(loadPos int64, roomIDs []string, maxTimelineEvents int) map[string]UserRoomData
	userID               string
	roomToData           map[string]UserRoomData
	roomToDataMu         *sync.RWMutex
	listeners            map[int]UserCacheListener
	listenersMu          *sync.Mutex
	id                   int
	store                *state.Storage
	globalCache          *GlobalCache
}

func NewUserCache(userID string, globalCache *GlobalCache, store *state.Storage) *UserCache {
	uc := &UserCache{
		userID:       userID,
		roomToDataMu: &sync.RWMutex{},
		roomToData:   make(map[string]UserRoomData),
		listeners:    make(map[int]UserCacheListener),
		listenersMu:  &sync.Mutex{},
		store:        store,
		globalCache:  globalCache,
	}
	return uc
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

func (c *UserCache) lazyLoadTimelines(loadPos int64, roomIDs []string, maxTimelineEvents int) map[string]UserRoomData {
	if c.LazyRoomDataOverride != nil {
		return c.LazyRoomDataOverride(loadPos, roomIDs, maxTimelineEvents)
	}
	result := make(map[string]UserRoomData)
	var lazyRoomIDs []string
	for _, roomID := range roomIDs {
		urd := c.loadRoomData(roomID)
		if len(urd.Timeline) > 0 {
			timeline := urd.Timeline
			if len(timeline) > maxTimelineEvents {
				timeline = timeline[len(timeline)-maxTimelineEvents:]
			}
			// we already have data, use it
			result[roomID] = UserRoomData{
				NotificationCount: urd.NotificationCount,
				HighlightCount:    urd.HighlightCount,
				Timeline:          timeline,
			}
		} else {
			lazyRoomIDs = append(lazyRoomIDs, roomID)
		}
	}
	if len(lazyRoomIDs) == 0 {
		return result
	}
	roomIDToEvents, err := c.store.LatestEventsInRooms(c.userID, lazyRoomIDs, loadPos, maxTimelineEvents)
	if err != nil {
		logger.Err(err).Strs("rooms", lazyRoomIDs).Msg("failed to get LatestEventsInRooms")
		return nil
	}
	c.roomToDataMu.Lock()
	for roomID, events := range roomIDToEvents {
		urd, ok := c.roomToData[roomID]
		if !ok {
			urd = UserRoomData{}
		}
		urd.Timeline = events

		result[roomID] = urd
		c.roomToData[roomID] = urd
	}
	c.roomToDataMu.Unlock()
	return result
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

func (c *UserCache) OnNewEvent(eventData *EventData) {
	// add this to our tracked timelines if we have one
	urd := c.loadRoomData(eventData.roomID)
	if len(urd.Timeline) > 0 {
		// we're tracking timelines, add this message too
		urd.Timeline = append(urd.Timeline, eventData.event)
	}
	c.roomToDataMu.Lock()
	c.roomToData[eventData.roomID] = urd
	c.roomToDataMu.Unlock()

	for _, l := range c.listeners {
		l.OnNewEvent(eventData)
	}
}
