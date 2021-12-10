package sync3

import (
	"encoding/json"
	"sync"

	"github.com/matrix-org/sync-v3/state"
	"github.com/tidwall/gjson"
)

type UserRoomData struct {
	IsDM              bool
	IsInvite          bool
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
	UserID               string
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
		UserID:       userID,
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

func (c *UserCache) LazyLoadTimelines(loadPos int64, roomIDs []string, maxTimelineEvents int) map[string]UserRoomData {
	if c.LazyRoomDataOverride != nil {
		return c.LazyRoomDataOverride(loadPos, roomIDs, maxTimelineEvents)
	}
	result := make(map[string]UserRoomData)
	var lazyRoomIDs []string
	for _, roomID := range roomIDs {
		urd := c.LoadRoomData(roomID)
		if len(urd.Timeline) > 0 {
			timeline := urd.Timeline
			if len(timeline) > maxTimelineEvents {
				timeline = timeline[len(timeline)-maxTimelineEvents:]
			}
			// ensure that if the user initially wants 1 event in this room then bumps up to 50 that
			// we actually give them 50. This is more than just a length check as the room may not
			// have 50 events, we can tell this based on the create event
			createEventExists := false
			if len(timeline) < maxTimelineEvents {
				for _, ev := range timeline {
					if gjson.GetBytes(ev, "type").Str == "m.room.create" && gjson.GetBytes(ev, "state_key").Str == "" {
						createEventExists = true
						break
					}
				}
			}

			// either we satisfied their request or we can't get any more events, either way that's good enough
			if len(timeline) == maxTimelineEvents || createEventExists {
				// we already have data, use it
				result[roomID] = UserRoomData{
					NotificationCount: urd.NotificationCount,
					HighlightCount:    urd.HighlightCount,
					Timeline:          timeline,
				}
			} else {
				// refetch from the db
				lazyRoomIDs = append(lazyRoomIDs, roomID)
			}
		} else {
			lazyRoomIDs = append(lazyRoomIDs, roomID)
		}
	}
	if len(lazyRoomIDs) == 0 {
		return result
	}
	roomIDToEvents, err := c.store.LatestEventsInRooms(c.UserID, lazyRoomIDs, loadPos, maxTimelineEvents)
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

func (c *UserCache) LoadRoomData(roomID string) UserRoomData {
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
	data := c.LoadRoomData(roomID)
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
		l.OnUnreadCountsChanged(c.UserID, roomID, data, hasCountDecreased)
	}
}

func (c *UserCache) OnNewEvent(eventData *EventData) {
	// add this to our tracked timelines if we have one
	urd := c.LoadRoomData(eventData.RoomID)
	if len(urd.Timeline) > 0 {
		// we're tracking timelines, add this message too
		urd.Timeline = append(urd.Timeline, eventData.Event)
	}
	c.roomToDataMu.Lock()
	c.roomToData[eventData.RoomID] = urd
	c.roomToDataMu.Unlock()

	for _, l := range c.listeners {
		l.OnNewEvent(eventData)
	}
}

func (c *UserCache) OnAccountData(datas []state.AccountData) {
	for _, d := range datas {
		if d.Type == "m.direct" {
			dmRoomSet := make(map[string]struct{})
			// pull out rooms and mark them as DMs
			content := gjson.ParseBytes(d.Data).Get("content")
			content.ForEach(func(_, v gjson.Result) bool {
				for _, roomIDResult := range v.Array() {
					dmRoomSet[roomIDResult.Str] = struct{}{}
				}
				return true
			})
			// this event REPLACES all DM rooms so reset the DM state on all rooms then update
			c.roomToDataMu.Lock()
			for roomID, urd := range c.roomToData {
				_, exists := dmRoomSet[roomID]
				urd.IsDM = exists
				c.roomToData[roomID] = urd
				delete(dmRoomSet, roomID)
			}
			// remaining stuff in dmRoomSet are new rooms the cache is unaware of
			for dmRoomID := range dmRoomSet {
				c.roomToData[dmRoomID] = UserRoomData{
					IsDM: true,
				}
			}
			c.roomToDataMu.Unlock()
		}
	}
}
