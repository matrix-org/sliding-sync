package caches

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/getsentry/sentry-go"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	InvitesAreHighlightsValue = 1 // invite -> highlight count = 1
)

type CacheFinder interface {
	CacheForUser(userID string) *UserCache
}

type TransactionIDFetcher interface {
	TransactionIDForEvents(userID, deviceID string, eventIDs []string) (eventIDToTxnID map[string]string)
}

type JoinChecker interface {
	IsUserJoined(userID, roomID string) bool
}

// UserRoomData describes a single room from the perspective of particular user.
// It is primarily used in two places:
//   - in the caches.UserCache, to hold the latest version of user-specific data; and
//   - in the sync3.RoomConnMetadata struct, to hold the version of data last seen by
//     a given sync connection.
//
// Roughly speaking, the sync3.RoomConnMetadata is constantly catching up with changes
// in the caches.UserCache.
type UserRoomData struct {
	IsDM              bool
	IsInvite          bool
	HasLeft           bool
	NotificationCount int
	HighlightCount    int
	Invite            *InviteData

	// TODO: should CanonicalisedName really be in RoomConMetadata? It's only set in SetRoom AFAICS
	CanonicalisedName string // stripped leading symbols like #, all in lower case
	// Set of spaces this room is a part of, from the perspective of this user. This is NOT global room data
	// as the set of spaces may be different for different users.

	// ResolvedAvatarURL is the avatar that should be displayed to this user to
	// represent this room. The empty string means that this room has no avatar.
	// Avatars set in m.room.avatar take precedence; if this is missing and the room is
	// a DM with one other user joined or invited, we fall back to that user's
	// avatar (if any) as specified in their membership event in that room.
	ResolvedAvatarURL string

	// Spaces is the set of room IDs of spaces that this room is part of.
	Spaces map[string]struct{}
	// Map of tag to order float.
	// See https://spec.matrix.org/latest/client-server-api/#room-tagging
	Tags map[string]float64
	// JoinTiming tracks our latest join to the room, excluding profile changes.
	JoinTiming internal.EventMetadata
}

func NewUserRoomData() UserRoomData {
	return UserRoomData{
		Spaces: make(map[string]struct{}),
		Tags:   make(map[string]float64),
	}
}

// Subset of data from internal.RoomMetadata which we can glean from invite_state.
// Processed in the same way as joined rooms!
type InviteData struct {
	roomID               string
	InviteState          []json.RawMessage
	Heroes               []internal.Hero
	InviteEvent          *EventData
	NameEvent            string // the content of m.room.name, NOT the calculated name
	AvatarEvent          string // the content of m.room.avatar, NOT the calculated avatar
	CanonicalAlias       string
	LastMessageTimestamp uint64
	Encrypted            bool
	IsDM                 bool
	RoomType             string
}

func NewInviteData(ctx context.Context, userID, roomID string, inviteState []json.RawMessage) *InviteData {
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
					Event:         ev,
					RoomID:        roomID,
					EventType:     "m.room.member",
					StateKey:      &target,
					Content:       j.Get("content"),
					Timestamp:     uint64(ts),
					AlwaysProcess: true,
				}
				id.IsDM = j.Get("content.is_direct").Bool()
			} else if target == j.Get("sender").Str {
				id.Heroes = append(id.Heroes, internal.Hero{
					ID:     target,
					Name:   j.Get("content.displayname").Str,
					Avatar: j.Get("content.avatar_url").Str,
				})
			}
		case "m.room.name":
			id.NameEvent = j.Get("content.name").Str
		case "m.room.avatar":
			id.AvatarEvent = j.Get("content.url").Str
		case "m.room.canonical_alias":
			id.CanonicalAlias = j.Get("content.alias").Str
		case "m.room.encryption":
			id.Encrypted = true
		case "m.room.create":
			id.RoomType = j.Get("content.type").Str
		}
	}
	if id.InviteEvent == nil {
		const errMsg = "cannot make invite, missing invite event for user"
		log.Error().Str("invitee", userID).Str("room", roomID).Int("num_invite_state", len(inviteState)).Msg(errMsg)
		hub := internal.GetSentryHubFromContextOrDefault(ctx)
		hub.WithScope(func(scope *sentry.Scope) {
			scope.SetContext(internal.SentryCtxKey, map[string]interface{}{
				"invitee":          userID,
				"room":             roomID,
				"num_invite_state": len(inviteState),
			})
			hub.CaptureException(fmt.Errorf(errMsg))
		})
		return nil
	}
	return &id
}

func (i *InviteData) RoomMetadata() *internal.RoomMetadata {
	var roomType *string
	if i.RoomType != "" {
		roomType = &i.RoomType
	}
	metadata := internal.NewRoomMetadata(i.roomID)
	metadata.Heroes = i.Heroes
	metadata.NameEvent = i.NameEvent
	metadata.AvatarEvent = i.AvatarEvent
	metadata.CanonicalAlias = i.CanonicalAlias
	metadata.InviteCount = 1
	metadata.JoinCount = 1
	metadata.LastMessageTimestamp = i.LastMessageTimestamp
	metadata.Encrypted = i.Encrypted
	metadata.RoomType = roomType
	return metadata
}

type UserCacheListener interface {
	// Called when there is an update affecting a room e.g new event, unread count update, room account data.
	// Type-cast to find out what the update is about.
	OnRoomUpdate(ctx context.Context, up RoomUpdate)
	// Called when there is an update affecting this user but not in the room e.g global account data, presence.
	// Type-cast to find out what the update is about.
	OnUpdate(ctx context.Context, up Update)
}

// Subset of store functions used by the user cache
type UserCacheStore interface {
	LatestEventsInRooms(userID string, roomIDs []string, to int64, limit int) (map[string]*state.LatestEvents, error)
	GetClosestPrevBatch(roomID string, eventNID int64) (prevBatch string)
}

// Tracks data specific to a given user. Specifically, this is the map of room ID to UserRoomData.
// This data is user-scoped, not global or connection scoped.
type UserCache struct {
	LazyLoadTimelinesOverride func(loadPos int64, roomIDs []string, maxTimelineEvents int) map[string]state.LatestEvents
	UserID                    string
	roomToData                map[string]UserRoomData
	roomToDataMu              *sync.RWMutex
	listeners                 map[int]UserCacheListener
	listenersMu               *sync.RWMutex
	id                        int
	store                     UserCacheStore
	globalCache               *GlobalCache
	txnIDs                    TransactionIDFetcher
	joinChecker               JoinChecker
	ignoredUsers              map[string]struct{}
	ignoredUsersMu            *sync.RWMutex
}

func NewUserCache(userID string, globalCache *GlobalCache, store UserCacheStore, txnIDs TransactionIDFetcher, joinChecker JoinChecker) *UserCache {
	// see SyncLiveHandler.userCache for the initialisation proper, which works by
	// firing off a bunch of OnBlahBlah callbacks.
	uc := &UserCache{
		UserID:         userID,
		roomToDataMu:   &sync.RWMutex{},
		roomToData:     make(map[string]UserRoomData),
		listeners:      make(map[int]UserCacheListener),
		listenersMu:    &sync.RWMutex{},
		store:          store,
		globalCache:    globalCache,
		txnIDs:         txnIDs,
		joinChecker:    joinChecker,
		ignoredUsers:   make(map[string]struct{}),
		ignoredUsersMu: &sync.RWMutex{},
	}
	return uc
}

func (c *UserCache) Subscribe(ucl UserCacheListener) (id int) {
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

// OnRegistered is called after the sync3.Dispatcher has successfully registered this
// cache to receive updates. We use this to run some final initialisation logic that
// is sensitive to race conditions; confusingly, most of the initialisation is driven
// externally by sync3.SyncLiveHandler.userCaches. It's important that we don't spend too
// long inside this function, because it is called within a global lock on the
// sync3.Dispatcher (see sync3.Dispatcher.Register).
func (c *UserCache) OnRegistered(ctx context.Context) error {
	// select all spaces the user is a part of to seed the cache correctly. This has to be done in
	// the OnRegistered callback which has locking guarantees. This is why...
	_, joinedRooms, joinTimings, _, err := c.globalCache.LoadJoinedRooms(ctx, c.UserID)
	if err != nil {
		return fmt.Errorf("failed to load joined rooms: %s", err)
	}

	// There is a race condition here as the global cache is a snapshot in time. If you register
	// AFTER querying the global cache, this happens:
	//
	//  UserCache         GlobalCache        Dispatcher
	//    |-loadJoinedRooms->|                    |
	//    |<----pos,rooms----|                    |
	//    |                  |<---new space event-|  THIS UPDATE GOES MISSING
	//    |--------------------register---------->|
	//
	// If you register BEFORE querying the global cache, this happens:
	//
	//  UserCache         GlobalCache        Dispatcher
	//    |--------------------register---------->|
	//    |                  |<---new space event-|  THIS UPDATE GET PROCESSED TWICE
	//    |<--------new space event---------------|
	//    |-loadJoinedRooms->|                    |
	//    |<----pos,rooms----|                    |
	//
	// Ideally we would atomically register with the dispatcher and assign the position in the stream so we can
	// guarantee exactly once processing, which is why we do this in the OnRegistered callback:
	//
	//  UserCache         GlobalCache        Dispatcher
	//    |--------------------register---------->| LOCK
	//    |<------OnRegistered(pos)---------------|
	//    |-loadJoinedRooms->|                    |
	//    |<----pos,rooms----|                    |
	//    |                  |                    | UNLOCK
	//    |                  |<---new space event-|  GENUINE NEW EVENT
	//    |<--------new space event---------------|
	//

	// the db pos is _always_ equal to or ahead of the dispatcher, so we will discard
	// any updates from the dispatcher with position less than this. This can happen even with the
	// OnRegistered lock because DB inserts are independent to LoadJoinedRooms, so it is possible
	// to load a newer version of rooms, and then see duplicate On... calls - it is the same problem
	// that ConnState has which is why it has loadPositions. However, unlike ConnState, these dupe updates
	// don't have any negative effect as we are just updating UserRoomData, not sending timeline events,
	// so we consciously let this race happen.
	for _, room := range joinedRooms {
		// inject space children events
		if room.IsSpace() {
			for childRoomID := range room.ChildSpaceRooms {
				c.OnSpaceUpdate(ctx, room.RoomID, childRoomID, false, &EventData{
					RoomID:    room.RoomID,
					EventType: "m.space.child",
					StateKey:  &childRoomID,
					NID:       0,
				})
			}
		}

		// Record when we joined the room. We've just had to scan the history of our
		// membership in this room to produce joinedRooms above, so we may as well
		// do this here too.
		c.roomToDataMu.Lock()
		urd, ok := c.roomToData[room.RoomID]
		if !ok {
			urd = NewUserRoomData()
		}
		urd.JoinTiming = joinTimings[room.RoomID]
		c.roomToData[room.RoomID] = urd
		c.roomToDataMu.Unlock()
	}
	return nil
}

// LazyLoadTimelines loads the most recent timeline events (up to `maxTimelineEvents`)
// for each of the given rooms from the database (plus other timeline-related data).
// Only events with NID <= loadPos are returned.
// Events from senders ignored by this user are dropped.
// Returns nil on error.
func (c *UserCache) LazyLoadTimelines(ctx context.Context, loadPos int64, roomIDs []string, maxTimelineEvents int) map[string]state.LatestEvents {
	_, span := internal.StartSpan(ctx, "LazyLoadTimelines")
	defer span.End()
	if c.LazyLoadTimelinesOverride != nil {
		return c.LazyLoadTimelinesOverride(loadPos, roomIDs, maxTimelineEvents)
	}
	result := make(map[string]state.LatestEvents)
	roomIDToLatestEvents, err := c.store.LatestEventsInRooms(c.UserID, roomIDs, loadPos, maxTimelineEvents)
	if err != nil {
		log.Err(err).Strs("rooms", roomIDs).Msg("failed to get LatestEventsInRooms")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return nil
	}
	for _, requestedRoomID := range roomIDs {
		latestEvents := roomIDToLatestEvents[requestedRoomID]
		if latestEvents != nil {
			latestEvents.DiscardIgnoredMessages(c.ShouldIgnore)
			result[requestedRoomID] = *latestEvents
		}
	}
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

// LoadRooms is a batch version of LoadRoomData. Returns a map keyed by roomID.
func (c *UserCache) LoadRooms(roomIDs ...string) map[string]UserRoomData {
	result := make(map[string]UserRoomData, len(roomIDs))
	c.roomToDataMu.RLock()
	defer c.roomToDataMu.RUnlock()
	for _, roomID := range roomIDs {
		data, ok := c.roomToData[roomID]
		if !ok {
			data = NewUserRoomData()
		}
		result[roomID] = data
	}
	return result
}

type roomUpdateCache struct {
	roomID string
	// globalRoomData is a snapshot of the global metadata for this room immediately
	// after this update. It is a copy, specific to the given user whose Heroes
	// field can be freely modified.
	globalRoomData *internal.RoomMetadata
	userRoomData   *UserRoomData
}

func (c *roomUpdateCache) Type() string {
	return "roomUpdateCache"
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
func (c *UserCache) newRoomUpdate(ctx context.Context, roomID string) RoomUpdate {
	u := c.LoadRoomData(roomID)
	var r *internal.RoomMetadata
	globalRooms := c.globalCache.LoadRooms(ctx, roomID)
	if globalRooms == nil || globalRooms[roomID] == nil {
		// this can happen when we join a room we didn't know about because we process unread counts
		// before the timeline events. Warn and send a stub
		log.Warn().Str("room", roomID).Msg("UserCache update: room doesn't exist in global cache yet, generating stub")
		r = internal.NewRoomMetadata(roomID)
	} else {
		r = globalRooms[roomID]
	}
	internal.AssertWithContext(ctx, "missing global room metadata for room "+roomID, r != nil)
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

// AttemptToFetchPrevBatch tries to find a prev_batch value for the given event. This may not always succeed.
func (c *UserCache) AttemptToFetchPrevBatch(ctx context.Context, roomID string, firstTimelineEvent *EventData) (prevBatch string) {
	_, span := internal.StartSpan(ctx, "AttemptToFetchPrevBatch")
	defer span.End()
	return c.store.GetClosestPrevBatch(roomID, firstTimelineEvent.NID)
}

// AnnotateWithTransactionIDs should be called just prior to returning events to the client. This
// will modify the events to insert the correct transaction IDs if needed. This is required because
// events are globally scoped, so if Alice sends a message, Bob might receive it first on his v2 loop
// which would cause the transaction ID to be missing from the event. Instead, we always look for txn
// IDs in the v2 poller, and then set them appropriately at request time.
func (c *UserCache) AnnotateWithTransactionIDs(ctx context.Context, userID string, deviceID string, roomIDToEvents map[string][]json.RawMessage) map[string][]json.RawMessage {
	_, span := internal.StartSpan(ctx, "AnnotateWithTransactionIDs")
	defer span.End()
	var eventIDs []string
	eventIDToEvent := make(map[string]struct {
		roomID string
		i      int
	})
	for roomID, events := range roomIDToEvents {
		for i, evJSON := range events {
			ev := gjson.ParseBytes(evJSON)
			evID := ev.Get("event_id").Str
			sender := ev.Get("sender").Str
			if sender != userID {
				// don't ask for txn IDs for events which weren't sent by us.
				// If we do, we'll needlessly hit the database, increasing latencies when
				// catching up from the live buffer.
				continue
			}
			eventIDs = append(eventIDs, evID)
			eventIDToEvent[evID] = struct {
				roomID string
				i      int
			}{
				roomID: roomID,
				i:      i,
			}
		}
	}
	if len(eventIDs) == 0 {
		// don't do any work if we have no events
		return roomIDToEvents
	}
	eventIDToTxnID := c.txnIDs.TransactionIDForEvents(userID, deviceID, eventIDs)
	for eventID, txnID := range eventIDToTxnID {
		data, ok := eventIDToEvent[eventID]
		if !ok {
			continue
		}
		events := roomIDToEvents[data.roomID]
		event := events[data.i]
		newJSON, err := sjson.SetBytes(event, "unsigned.transaction_id", txnID)
		if err != nil {
			log.Err(err).Str("user", c.UserID).Msg("AnnotateWithTransactionIDs: sjson failed")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		} else {
			events[data.i] = newJSON
			roomIDToEvents[data.roomID] = events
		}
	}
	return roomIDToEvents
}

// =================================================
// Listener functions called by v2 pollers are below
// =================================================

func (c *UserCache) OnEphemeralEvent(ctx context.Context, roomID string, ephEvent json.RawMessage) {
	var update RoomUpdate
	evType := gjson.GetBytes(ephEvent, "type").Str
	switch evType {
	case "m.typing":
		update = &TypingUpdate{
			RoomUpdate: c.newRoomUpdate(ctx, roomID),
		}
	}
	if update == nil {
		return
	}

	c.emitOnRoomUpdate(ctx, update)
}

func (c *UserCache) OnReceipt(ctx context.Context, receipt internal.Receipt) {
	c.emitOnRoomUpdate(ctx, &ReceiptUpdate{
		RoomUpdate: c.newRoomUpdate(ctx, receipt.RoomID),
		Receipt:    receipt,
	})
}

func (c *UserCache) emitOnRoomUpdate(ctx context.Context, update RoomUpdate) {
	c.listenersMu.RLock()
	var listeners []UserCacheListener
	for _, l := range c.listeners {
		listeners = append(listeners, l)
	}
	c.listenersMu.RUnlock()
	for _, l := range listeners {
		l.OnRoomUpdate(ctx, update)
	}
}

func (c *UserCache) emitOnUpdate(ctx context.Context, update Update) {
	c.listenersMu.RLock()
	var listeners []UserCacheListener
	for _, l := range c.listeners {
		listeners = append(listeners, l)
	}
	c.listenersMu.RUnlock()
	for _, l := range listeners {
		l.OnUpdate(ctx, update)
	}
}

func (c *UserCache) OnUnreadCounts(ctx context.Context, roomID string, highlightCount, notifCount *int) {
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
		RoomUpdate:        c.newRoomUpdate(ctx, roomID),
		HasCountDecreased: hasCountDecreased,
	}

	c.emitOnRoomUpdate(ctx, roomUpdate)
}

func (c *UserCache) OnSpaceUpdate(ctx context.Context, parentRoomID, childRoomID string, isDeleted bool, eventData *EventData) {
	childURD := c.LoadRoomData(childRoomID)
	if isDeleted {
		delete(childURD.Spaces, parentRoomID)
	} else {
		childURD.Spaces[parentRoomID] = struct{}{}
	}
	c.roomToDataMu.Lock()
	c.roomToData[childRoomID] = childURD
	c.roomToDataMu.Unlock()

	// now we need to notify connections for the _child_
	// but only if they are allowed to see the child event (i.e they are joined to the child room)
	if c.joinChecker.IsUserJoined(c.UserID, childRoomID) {
		roomUpdate := &RoomEventUpdate{
			RoomUpdate: c.newRoomUpdate(ctx, childRoomID),
			EventData:  eventData,
		}
		c.emitOnRoomUpdate(ctx, roomUpdate)
	}
}

func (c *UserCache) OnNewEvent(ctx context.Context, eventData *EventData) {
	// add this to our tracked timelines if we have one
	urd := c.LoadRoomData(eventData.RoomID)
	// reset the IsInvite field when the user actually joins/rejects the invite
	if urd.IsInvite && eventData.EventType == "m.room.member" && eventData.StateKey != nil && *eventData.StateKey == c.UserID {
		urd.IsInvite = eventData.Content.Get("membership").Str == "invite"
		if !urd.IsInvite {
			urd.HighlightCount = 0
		}
	}
	if eventData.EventType == "m.space.child" && eventData.StateKey != nil {
		// the children for a space we are a part of have changed. Find the room that was affected and update our cache value.
		childRoomID := *eventData.StateKey
		isDeleted := !eventData.Content.Get("via").IsArray()
		c.OnSpaceUpdate(ctx, eventData.RoomID, childRoomID, isDeleted, eventData)
	}
	c.roomToDataMu.Lock()
	c.roomToData[eventData.RoomID] = urd
	c.roomToDataMu.Unlock()

	roomUpdate := &RoomEventUpdate{
		RoomUpdate: c.newRoomUpdate(ctx, eventData.RoomID),
		EventData:  eventData,
	}

	c.emitOnRoomUpdate(ctx, roomUpdate)
}

func (c *UserCache) OnInvite(ctx context.Context, roomID string, inviteStateEvents []json.RawMessage) {
	inviteData := NewInviteData(ctx, c.UserID, roomID, inviteStateEvents)
	if inviteData == nil {
		return // malformed invite
	}

	urd := c.LoadRoomData(roomID)
	urd.IsInvite = true
	urd.HasLeft = false
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
	c.emitOnRoomUpdate(ctx, up)
}

func (c *UserCache) OnLeftRoom(ctx context.Context, roomID string, leaveEvent json.RawMessage) {
	urd := c.LoadRoomData(roomID)
	wasInvite := urd.IsInvite
	urd.IsInvite = false
	urd.HasLeft = true
	urd.Invite = nil
	urd.HighlightCount = 0
	c.roomToDataMu.Lock()
	c.roomToData[roomID] = urd
	c.roomToDataMu.Unlock()

	ev := gjson.ParseBytes(leaveEvent)
	stateKey := ev.Get("state_key").Str
	sender := ev.Get("sender").Str
	evType := ev.Get("type").Str

	// If the event in question is a kick, we should AlwaysProcess this to make sure the client
	// knows about the "leave"
	isKick := false
	if evType == "m.room.member" && ev.Get("content.membership").Str == "leave" && stateKey != sender {
		isKick = true
	}

	up := &RoomEventUpdate{
		RoomUpdate: &roomUpdateCache{
			roomID: roomID,
			// do NOT pull from the global cache as it is a snapshot of the room at the point of
			// the invite: don't leak additional data!!!
			globalRoomData: internal.NewRoomMetadata(roomID),
			userRoomData:   &urd,
		},
		EventData: &EventData{
			Event:     leaveEvent,
			RoomID:    roomID,
			EventType: evType,
			StateKey:  &stateKey,
			Content:   ev.Get("content"),
			Timestamp: ev.Get("origin_server_ts").Uint(),
			Sender:    sender,
			// if this is an invite rejection/a kick we need to make sure we tell the client, and not
			// skip it because of the lack of a NID (this event may not be in the events table)
			AlwaysProcess: wasInvite || isKick,
		},
	}
	c.emitOnRoomUpdate(ctx, up)
}

func (c *UserCache) OnAccountData(ctx context.Context, datas []state.AccountData) {
	roomUpdates := make(map[string][]state.AccountData)
	// room_id -> tag_id -> order
	tagUpdates := make(map[string]map[string]float64)
	for _, d := range datas {
		up := roomUpdates[d.RoomID]
		up = append(up, d)
		roomUpdates[d.RoomID] = up
		switch d.Type {
		case "m.direct":
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
		case "m.tag":
			content := gjson.ParseBytes(d.Data).Get("content.tags")
			if tagUpdates[d.RoomID] == nil {
				tagUpdates[d.RoomID] = make(map[string]float64)
			}
			content.ForEach(func(k, v gjson.Result) bool {
				tagUpdates[d.RoomID][k.Str] = v.Get("order").Float()
				return true
			})
		case "m.ignored_user_list":
			if d.RoomID != state.AccountDataGlobalRoom {
				continue
			}
			content := gjson.ParseBytes(d.Data).Get("content.ignored_users")
			if !content.IsObject() {
				continue
			}
			ignoredUsers := make(map[string]struct{})
			content.ForEach(func(k, v gjson.Result) bool {
				ignoredUsers[k.Str] = struct{}{}
				return true
			})
			c.ignoredUsersMu.Lock()
			c.ignoredUsers = ignoredUsers
			c.ignoredUsersMu.Unlock()
		}
	}
	if len(tagUpdates) > 0 {
		c.roomToDataMu.Lock()
		// bulk assign tag updates
		for roomID, tags := range tagUpdates {
			urd, ok := c.roomToData[roomID]
			if !ok {
				urd = NewUserRoomData()
			}
			urd.Tags = tags
			c.roomToData[roomID] = urd
		}
		c.roomToDataMu.Unlock()
	}
	// bucket account data updates per-room and globally then invoke listeners
	for roomID, updates := range roomUpdates {
		if roomID == state.AccountDataGlobalRoom {
			globalUpdate := &AccountDataUpdate{
				AccountData: updates,
			}
			c.emitOnUpdate(ctx, globalUpdate)
		} else {
			roomUpdate := &RoomAccountDataUpdate{
				AccountData: updates,
				RoomUpdate:  c.newRoomUpdate(ctx, roomID),
			}
			c.emitOnRoomUpdate(ctx, roomUpdate)
		}
	}

}

func (u *UserCache) ShouldIgnore(userID string) bool {
	u.ignoredUsersMu.RLock()
	defer u.ignoredUsersMu.RUnlock()
	_, ignored := u.ignoredUsers[userID]
	return ignored
}
