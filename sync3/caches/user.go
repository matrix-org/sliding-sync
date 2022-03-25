package caches

import (
	"encoding/json"
	"sync"

	"github.com/matrix-org/sync-v3/internal"
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
	// Called when there is an update affecting a room e.g new event, unread count update, room account data.
	// Type-cast to find out what the update is about.
	OnRoomUpdate(up RoomUpdate)
	// Called when there is an update affecting this user but not in the room e.g global account data, presence.
	// Type-cast to find out what the update is about.
	OnUpdate(up Update)
}

// Tracks data specific to a given user. Specifically, this is the map of room ID to UserRoomData.
// This data is user-scoped, not global or connection scoped.
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

// Mark these rooms as being invites.
func (c *UserCache) MarkInvites(rooms ...*internal.RoomMetadata) {
	c.roomToDataMu.Lock()
	defer c.roomToDataMu.Unlock()
	for _, room := range rooms {
		data, ok := c.roomToData[room.RoomID]
		if !ok {
			data = UserRoomData{}
		}
		data.IsInvite = true
		c.roomToData[room.RoomID] = data
	}
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

type roomUpdateCache struct {
	roomID         string
	globalRoomData *internal.RoomMetadata
	userRoomData   *UserRoomData
}

func (c *roomUpdateCache) RoomID() string {
	return c.roomID
}
func (c *roomUpdateCache) GlobalRoomMetadata() *internal.RoomMetadata {
	return c.globalRoomData
}
func (c *roomUpdateCache) UserRoomMetadata() *UserRoomData {
	return c.userRoomData
}

// snapshots the user cache / global cache data for this room for sending to connections
func (c *UserCache) newRoomUpdate(roomID string) RoomUpdate {
	u := c.LoadRoomData(roomID)
	var r *internal.RoomMetadata
	globalRooms := c.globalCache.LoadRooms(roomID)
	if globalRooms == nil {
		// this can happen when we join a room we didn't know about because we process unread counts
		// before the timeline events. Warn and send a stub
		logger.Warn().Str("room", roomID).Msg("UserCache update: room doesn't exist in global cache yet, generating stub")
		r = &internal.RoomMetadata{
			RoomID: roomID,
		}
	} else {
		r = globalRooms[0]
	}
	return &roomUpdateCache{
		roomID:         roomID,
		globalRoomData: r,
		userRoomData:   &u,
	}
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

	roomUpdate := &UnreadCountUpdate{
		RoomUpdate:        c.newRoomUpdate(roomID),
		HasCountDecreased: hasCountDecreased,
	}

	for _, l := range c.listeners {
		l.OnRoomUpdate(roomUpdate)
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

	roomUpdate := &RoomEventUpdate{
		RoomUpdate: c.newRoomUpdate(eventData.RoomID),
		EventData:  eventData,
	}

	for _, l := range c.listeners {
		l.OnRoomUpdate(roomUpdate)
	}
}

func (c *UserCache) OnAccountData(datas []state.AccountData) {
	roomUpdates := make(map[string][]state.AccountData)
	for _, d := range datas {
		up := roomUpdates[d.RoomID]
		up = append(up, d)
		roomUpdates[d.RoomID] = up
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
	// bucket account data updates per-room and globally then invoke listeners
	for roomID, updates := range roomUpdates {
		if roomID == state.AccountDataGlobalRoom {
			globalUpdate := &AccountDataUpdate{
				AccountData: updates,
			}
			for _, l := range c.listeners {
				l.OnUpdate(globalUpdate)
			}
		} else {
			roomUpdate := &RoomAccountDataUpdate{
				AccountData: updates,
				RoomUpdate:  c.newRoomUpdate(roomID),
			}
			for _, l := range c.listeners {
				l.OnRoomUpdate(roomUpdate)
			}
		}
	}

}
