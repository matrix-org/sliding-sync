package caches

import (
	"encoding/json"
	"sync"

	lru "github.com/hashicorp/golang-lru"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	InvitesAreHighlightsValue = 1 // invite -> highlight count = 1
)

type UserRoomData struct {
	IsDM              bool
	IsInvite          bool
	NotificationCount int
	HighlightCount    int
	// (event_id, last_event_id) -> closest prev_batch
	// We mux in last_event_id so we can invalidate prev batch tokens for the same event ID when a new timeline event
	// comes in, without having to do a SQL query.
	PrevBatches *lru.Cache
	Timeline    []json.RawMessage
	Invite      *InviteData
}

func NewUserRoomData() UserRoomData {
	l, _ := lru.New(64) // 64 tokens least recently used evicted
	return UserRoomData{
		PrevBatches: l,
	}
}

// fetch the prev batch for this timeline
func (u UserRoomData) PrevBatch() (string, bool) {
	if len(u.Timeline) == 0 {
		return "", false
	}
	eventID := gjson.GetBytes(u.Timeline[0], "event_id").Str
	lastEventID := gjson.GetBytes(u.Timeline[len(u.Timeline)-1], "event_id").Str
	val, ok := u.PrevBatches.Get(eventID + lastEventID)
	if !ok {
		return "", false
	}
	return val.(string), true
}

// set the prev batch token for the given event ID. This should come from the database. The prev batch
// cache will be updated to return this prev batch token for this event ID as well as the latest event
// ID in this timeline.
func (u UserRoomData) SetPrevBatch(eventID string, pb string) {
	if len(u.Timeline) == 0 {
		return
	}
	lastEventID := gjson.GetBytes(u.Timeline[len(u.Timeline)-1], "event_id").Str
	u.PrevBatches.Add(eventID+lastEventID, pb)
	u.PrevBatches.Add(eventID+eventID, pb)
}

// Subset of data from internal.RoomMetadata which we can glean from invite_state.
// Processed in the same way as joined rooms!
type InviteData struct {
	roomID               string
	InviteState          []json.RawMessage
	Heroes               []internal.Hero
	InviteEvent          *EventData
	NameEvent            string // the content of m.room.name, NOT the calculated name
	CanonicalAlias       string
	LastMessageTimestamp uint64
	Encrypted            bool
	IsDM                 bool
}

func NewInviteData(userID, roomID string, inviteState []json.RawMessage) *InviteData {
	// work out metadata for this invite. There's an origin_server_ts on the invite m.room.member event
	id := InviteData{
		roomID:      roomID,
		InviteState: inviteState,
	}
	for _, ev := range inviteState {
		j := gjson.ParseBytes(ev)

		switch j.Get("type").Str {
		case "m.room.member":
			target := j.Get("state_key").Str
			if userID == target {
				// this is our invite event; grab the timestamp
				ts := j.Get("origin_server_ts").Int()
				id.LastMessageTimestamp = uint64(ts)
				id.InviteEvent = &EventData{
					Event:     ev,
					RoomID:    roomID,
					EventType: "m.room.member",
					StateKey:  &target,
					Content:   j.Get("content"),
					Timestamp: uint64(ts),
					LatestPos: PosAlwaysProcess,
				}
				id.IsDM = j.Get("is_direct").Bool()
			} else if target == j.Get("sender").Str {
				id.Heroes = append(id.Heroes, internal.Hero{
					ID:   target,
					Name: j.Get("content.displayname").Str,
				})
			}
		case "m.room.name":
			id.NameEvent = j.Get("content.name").Str
		case "m.room.canonical_alias":
			id.CanonicalAlias = j.Get("content.alias").Str
		case "m.room.encryption":
			id.Encrypted = true
		}
	}
	if id.InviteEvent == nil {
		logger.Error().Str("invitee", userID).Str("room", roomID).Int("num_invite_state", len(inviteState)).Msg(
			"cannot make invite, missing invite event for user",
		)
		return nil
	}
	return &id
}

func (i *InviteData) RoomMetadata() *internal.RoomMetadata {
	return &internal.RoomMetadata{
		RoomID:               i.roomID,
		Heroes:               i.Heroes,
		NameEvent:            i.NameEvent,
		CanonicalAlias:       i.CanonicalAlias,
		InviteCount:          1,
		JoinCount:            1,
		LastMessageTimestamp: i.LastMessageTimestamp,
		Encrypted:            i.Encrypted,
		Tombstoned:           false,
	}
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
	txnIDs               sync2.TransactionIDFetcher
}

func NewUserCache(userID string, globalCache *GlobalCache, store *state.Storage, txnIDs sync2.TransactionIDFetcher) *UserCache {
	uc := &UserCache{
		UserID:       userID,
		roomToDataMu: &sync.RWMutex{},
		roomToData:   make(map[string]UserRoomData),
		listeners:    make(map[int]UserCacheListener),
		listenersMu:  &sync.Mutex{},
		store:        store,
		globalCache:  globalCache,
		txnIDs:       txnIDs,
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
				if !createEventExists && len(timeline) > 0 {
					// fetch a prev batch token for the earliest event
					_, ok := urd.PrevBatch()
					if !ok {
						eventID := gjson.ParseBytes(timeline[0]).Get("event_id").Str
						prevBatch, err := c.store.EventsTable.SelectClosestPrevBatchByID(roomID, eventID)
						if err != nil {
							logger.Err(err).Str("room", roomID).Str("event_id", eventID).Msg("failed to get prev batch token for room")
						}
						urd.SetPrevBatch(eventID, prevBatch)
					}
				}

				// we already have data, use it
				u := NewUserRoomData()
				u.NotificationCount = urd.NotificationCount
				u.HighlightCount = urd.HighlightCount
				u.Timeline = timeline
				u.PrevBatches = urd.PrevBatches
				result[roomID] = u
			} else {
				// refetch from the db
				lazyRoomIDs = append(lazyRoomIDs, roomID)
			}
		} else {
			lazyRoomIDs = append(lazyRoomIDs, roomID)
			// in case the room is left/invited, we may not add them to the result here so do it now,
			// we'll clobber if we get a timeline
			result[roomID] = urd
		}
	}
	if len(lazyRoomIDs) == 0 {
		return result
	}
	roomIDToEvents, roomIDToPrevBatch, err := c.store.LatestEventsInRooms(c.UserID, lazyRoomIDs, loadPos, maxTimelineEvents)
	if err != nil {
		logger.Err(err).Strs("rooms", lazyRoomIDs).Msg("failed to get LatestEventsInRooms")
		return nil
	}
	c.roomToDataMu.Lock()
	for roomID, events := range roomIDToEvents {
		urd, ok := c.roomToData[roomID]
		if !ok {
			urd = NewUserRoomData()
		}
		urd.Timeline = events
		if len(events) > 0 {
			eventID := gjson.ParseBytes(events[0]).Get("event_id").Str
			urd.SetPrevBatch(eventID, roomIDToPrevBatch[roomID])
		}

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
		return NewUserRoomData()
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
		r = globalRooms[roomID]
	}
	return &roomUpdateCache{
		roomID:         roomID,
		globalRoomData: r,
		userRoomData:   &u,
	}
}

func (c *UserCache) Invites() map[string]UserRoomData {
	c.roomToDataMu.Lock()
	defer c.roomToDataMu.Unlock()
	invites := make(map[string]UserRoomData)
	for roomID, urd := range c.roomToData {
		if !urd.IsInvite || urd.Invite == nil {
			continue
		}
		invites[roomID] = urd
	}
	return invites
}

// AnnotateWithTransactionIDs should be called just prior to returning events to the client. This
// will modify the events to insert the correct transaction IDs if needed. This is required because
// events are globally scoped, so if Alice sends a message, Bob might receive it first on his v2 loop
// which would cause the transaction ID to be missing from the event. Instead, we always look for txn
// IDs in the v2 poller, and then set them appropriately at request time.
func (c *UserCache) AnnotateWithTransactionIDs(events []json.RawMessage) []json.RawMessage {
	for i := range events {
		eventID := gjson.GetBytes(events[i], "event_id")
		txnID := c.txnIDs.TransactionIDForEvent(c.UserID, eventID.Str)
		if txnID != "" {
			newJSON, err := sjson.SetBytes(events[i], "unsigned.transaction_id", txnID)
			if err != nil {
				logger.Err(err).Str("user", c.UserID).Msg("AnnotateWithTransactionIDs: sjson failed")
			} else {
				events[i] = newJSON
			}
		}
	}
	return events
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
	// reset the IsInvite field when the user actually joins/rejects the invite
	if urd.IsInvite && eventData.EventType == "m.room.member" && eventData.StateKey != nil && *eventData.StateKey == c.UserID {
		urd.IsInvite = eventData.Content.Get("membership").Str == "invite"
		if !urd.IsInvite {
			urd.HighlightCount = 0
		}
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

func (c *UserCache) OnInvite(roomID string, inviteStateEvents []json.RawMessage) {
	inviteData := NewInviteData(c.UserID, roomID, inviteStateEvents)
	if inviteData == nil {
		return // malformed invite
	}

	urd := c.LoadRoomData(roomID)
	urd.IsInvite = true
	urd.HighlightCount = InvitesAreHighlightsValue
	urd.IsDM = inviteData.IsDM
	urd.Invite = inviteData
	c.roomToDataMu.Lock()
	c.roomToData[roomID] = urd
	c.roomToDataMu.Unlock()

	up := &InviteUpdate{
		RoomUpdate: &roomUpdateCache{
			roomID: roomID,
			// do NOT pull from the global cache as it is a snapshot of the room at the point of
			// the invite: don't leak additional data!!!
			globalRoomData: inviteData.RoomMetadata(),
			userRoomData:   &urd,
		},
		InviteData: *inviteData,
	}
	for _, l := range c.listeners {
		l.OnRoomUpdate(up)
	}
}

func (c *UserCache) OnRetireInvite(roomID string) {
	urd := c.LoadRoomData(roomID)
	urd.IsInvite = false
	urd.Invite = nil
	urd.HighlightCount = 0
	c.roomToDataMu.Lock()
	c.roomToData[roomID] = urd
	c.roomToDataMu.Unlock()

	up := &InviteUpdate{
		RoomUpdate: &roomUpdateCache{
			roomID: roomID,
			// do NOT pull from the global cache as it is a snapshot of the room at the point of
			// the invite: don't leak additional data!!!
			globalRoomData: &internal.RoomMetadata{
				RoomID: roomID,
			},
			userRoomData: &urd,
		},
		Retired: true,
	}
	for _, l := range c.listeners {
		l.OnRoomUpdate(up)
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
				u := NewUserRoomData()
				u.IsDM = true
				c.roomToData[dmRoomID] = u
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
