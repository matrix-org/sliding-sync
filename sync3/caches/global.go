package caches

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

type EventData struct {
	Event     json.RawMessage
	RoomID    string
	EventType string
	StateKey  *string
	Content   gjson.Result
	Timestamp uint64
	Sender    string

	// the number of joined users in this room. Use this value and don't try to work it out as you
	// may get it wrong due to Synapse sending duplicate join events(!) This value has them de-duped
	// correctly.
	JoinCount   int
	InviteCount int

	// NID is the nid for this event; or a non-nid sentinel value. Current sentinels are
	//  - PosAlwaysProcess and PosDoNotProcess, for things outside the event timeline
	//    e.g invites; and
	//  - `0` used by UserCache.OnRegistered to inject space children events at startup.
	//    It's referenced in ConnState.OnRoomUpdateand UserCache.OnSpaceUpdate
	NID int64

	// Update consumers will usually ignore updates with a NID < what they have seen before for this room.
	// However, in some cases, we want to force the consumer to process this event even though the NID
	// may be < what they have seen before. Currently, we use this to force consumers to process invites
	// for a connected client, as the invite itself has no event NID due to the proxy not being in the room yet.
	AlwaysProcess bool

	// Flag set when this event should force the room contents to be resent e.g
	// state res, initial join, etc
	ForceInitial bool
}

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

// The purpose of global cache is to store global-level information about all rooms the server is aware of.
// Global-level information is represented as internal.RoomMetadata and includes things like Heroes, join/invite
// counts, if the room is encrypted, etc. Basically anything that is the same for all users of the system. This
// information is populated at startup from the database and then kept up-to-date by hooking into the
// Dispatcher for new events.
type GlobalCache struct {
	// LoadJoinedRoomsOverride allows tests to mock out the behaviour of LoadJoinedRooms.
	LoadJoinedRoomsOverride func(userID string) (pos int64, joinedRooms map[string]*internal.RoomMetadata, joinTimings map[string]internal.EventMetadata, latestNIDs map[string]int64, err error)

	// inserts are done by v2 poll loops, selects are done by v3 request threads
	// there are lots of overlapping keys as many users (threads) can be joined to the same room (key)
	// hence you must lock this with `mu` before r/w
	roomIDToMetadata   map[string]*internal.RoomMetadata
	roomIDToMetadataMu *sync.RWMutex

	// for loading room state not held in-memory TODO: remove to another struct along with associated functions
	store *state.Storage
}

func NewGlobalCache(store *state.Storage) *GlobalCache {
	return &GlobalCache{
		roomIDToMetadataMu: &sync.RWMutex{},
		store:              store,
		roomIDToMetadata:   make(map[string]*internal.RoomMetadata),
	}
}

func (c *GlobalCache) OnRegistered(_ context.Context) error {
	return nil
}

// LoadRooms loads the current room metadata for the given room IDs. Races unless you call this in a dispatcher loop.
// Always returns copies of the room metadata so ownership can be passed to other threads.
func (c *GlobalCache) LoadRooms(ctx context.Context, roomIDs ...string) map[string]*internal.RoomMetadata {
	c.roomIDToMetadataMu.RLock()
	defer c.roomIDToMetadataMu.RUnlock()
	result := make(map[string]*internal.RoomMetadata, len(roomIDs))
	for i := range roomIDs {
		roomID := roomIDs[i]
		result[roomID] = c.copyRoom(roomID)
	}
	return result
}

// LoadRoomsFromMap is like LoadRooms, except it is given a map with room IDs as keys
// and returns rooms in a map. The output map is non-nil and contains exactly the same
// set of keys as the input map. The values in the input map are completely ignored.
func (c *GlobalCache) LoadRoomsFromMap(ctx context.Context, joinTimingsByRoomID map[string]internal.EventMetadata) map[string]*internal.RoomMetadata {
	c.roomIDToMetadataMu.RLock()
	defer c.roomIDToMetadataMu.RUnlock()
	result := make(map[string]*internal.RoomMetadata, len(joinTimingsByRoomID))
	for roomID, _ := range joinTimingsByRoomID {
		result[roomID] = c.copyRoom(roomID)
	}
	return result
}

// copyRoom returns a copy of the internal.RoomMetadata stored for this room.
// This is an internal implementation detail of LoadRooms and LoadRoomsFromMap.
// If the room is not present in the global cache, returns a stub metadata entry.
// The caller MUST acquire a read lock on roomIDToMetadataMu before calling this.
func (c *GlobalCache) copyRoom(roomID string) *internal.RoomMetadata {
	sr := c.roomIDToMetadata[roomID]
	if sr == nil {
		logger.Warn().Str("room", roomID).Msg("GlobalCache.LoadRoom: no metadata for this room, returning stub")
		return internal.NewRoomMetadata(roomID)
	}
	srCopy := *sr
	// copy the heroes or else we may modify the same slice which would be bad :(
	srCopy.Heroes = make([]internal.Hero, len(sr.Heroes))
	for i := range sr.Heroes {
		srCopy.Heroes[i] = sr.Heroes[i]
	}
	return &srCopy
}

// LoadJoinedRooms loads all current joined room metadata for the user given, together
// with timing info for the user's latest join (excluding profile changes) to the room.
// Returns the absolute database position (the latest event NID across the whole DB),
// along with the results.
//
// The two maps returned by this function have exactly the same set of keys. Each is nil
// iff a non-nil error is returned.
// TODO: remove with LoadRoomState?
// FIXME: return args are a mess
func (c *GlobalCache) LoadJoinedRooms(ctx context.Context, userID string) (
	pos int64, joinedRooms map[string]*internal.RoomMetadata, joinTimingByRoomID map[string]internal.EventMetadata,
	latestNIDs map[string]int64, err error,
) {
	if c.LoadJoinedRoomsOverride != nil {
		return c.LoadJoinedRoomsOverride(userID)
	}
	initialLoadPosition, err := c.store.LatestEventNID()
	if err != nil {
		return 0, nil, nil, nil, err
	}
	joinTimingByRoomID, err = c.store.JoinedRoomsAfterPosition(userID, initialLoadPosition)
	if err != nil {
		return 0, nil, nil, nil, err
	}
	roomIDs := make([]string, len(joinTimingByRoomID))
	i := 0
	for roomID := range joinTimingByRoomID {
		roomIDs[i] = roomID
		i++
	}

	latestNIDs, err = c.store.EventsTable.LatestEventNIDInRooms(roomIDs, initialLoadPosition)
	if err != nil {
		return 0, nil, nil, nil, err
	}

	// TODO: no guarantee that this state is the same as latest unless called in a dispatcher loop
	rooms := c.LoadRoomsFromMap(ctx, joinTimingByRoomID)
	return initialLoadPosition, rooms, joinTimingByRoomID, latestNIDs, nil
}

func (c *GlobalCache) LoadStateEvent(ctx context.Context, roomID string, loadPosition int64, evType, stateKey string) json.RawMessage {
	roomIDToStateEvents, err := c.store.RoomStateAfterEventPosition(ctx, []string{roomID}, loadPosition, map[string][]string{
		evType: {stateKey},
	})
	if err != nil {
		logger.Err(err).Str("room", roomID).Int64("pos", loadPosition).Msg("failed to load room state")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return nil
	}
	events := roomIDToStateEvents[roomID]
	if len(events) > 0 {
		return events[0].JSON
	}
	return nil
}

// TODO: remove? Doesn't touch global cache fields
func (c *GlobalCache) LoadRoomState(ctx context.Context, roomIDs []string, loadPosition int64, requiredStateMap *internal.RequiredStateMap, roomToUsersInTimeline map[string][]string) map[string][]json.RawMessage {
	if c.store == nil {
		return nil
	}
	if requiredStateMap.Empty() {
		return nil
	}
	resultMap := make(map[string][]json.RawMessage, len(roomIDs))
	roomIDToStateEvents, err := c.store.RoomStateAfterEventPosition(ctx, roomIDs, loadPosition, requiredStateMap.QueryStateMap())
	if err != nil {
		logger.Err(err).Strs("rooms", roomIDs).Int64("pos", loadPosition).Msg("failed to load room state")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return nil
	}
	for roomID, stateEvents := range roomIDToStateEvents {
		var result []json.RawMessage
		for _, ev := range stateEvents {
			if requiredStateMap.Include(ev.Type, ev.StateKey) {
				result = append(result, ev.JSON)
			} else if requiredStateMap.IsLazyLoading() {
				usersInTimeline := roomToUsersInTimeline[roomID]
				for _, userID := range usersInTimeline {
					if ev.StateKey == userID {
						result = append(result, ev.JSON)
					}
				}
			}
		}
		resultMap[roomID] = result
	}
	// TODO: cache?
	return resultMap
}

// Startup will populate the cache with the provided metadata.
// Must be called prior to starting any v2 pollers else this operation can race. Consider:
//   - V2 poll loop started early
//   - Join event arrives, NID=50
//   - PopulateGlobalCache loads the latest NID=50, processes this join event in the process
//   - OnNewEvents is called with the join event
//   - join event is processed twice.
func (c *GlobalCache) Startup(roomIDToMetadata map[string]internal.RoomMetadata) error {
	c.roomIDToMetadataMu.Lock()
	defer c.roomIDToMetadataMu.Unlock()
	// sort room IDs for ease of debugging and for determinism
	roomIDs := make([]string, len(roomIDToMetadata))
	i := 0
	for r := range roomIDToMetadata {
		roomIDs[i] = r
		i++
	}
	sort.Strings(roomIDs)
	for _, roomID := range roomIDs {
		metadata := roomIDToMetadata[roomID]
		internal.Assert("room ID is set", metadata.RoomID != "")
		internal.Assert("last message timestamp exists", metadata.LastMessageTimestamp > 1)
		c.roomIDToMetadata[roomID] = &metadata
	}
	return nil
}

// =================================================
// Listener function called by dispatcher below
// =================================================

func (c *GlobalCache) OnEphemeralEvent(ctx context.Context, roomID string, ephEvent json.RawMessage) {
	evType := gjson.ParseBytes(ephEvent).Get("type").Str
	c.roomIDToMetadataMu.Lock()
	defer c.roomIDToMetadataMu.Unlock()
	metadata := c.roomIDToMetadata[roomID]
	if metadata == nil {
		metadata = internal.NewRoomMetadata(roomID)
	}

	switch evType {
	case "m.typing":
		metadata.TypingEvent = ephEvent
	}
	c.roomIDToMetadata[roomID] = metadata
}

func (c *GlobalCache) OnReceipt(ctx context.Context, receipt internal.Receipt) {
	// nothing to do but we need it because the Dispatcher demands it.
}

func (c *GlobalCache) OnNewEvent(
	ctx context.Context, ed *EventData,
) {
	// update global state
	c.roomIDToMetadataMu.Lock()
	defer c.roomIDToMetadataMu.Unlock()
	metadata := c.roomIDToMetadata[ed.RoomID]
	if metadata == nil {
		metadata = internal.NewRoomMetadata(ed.RoomID)
	}
	switch ed.EventType {
	case "m.room.name":
		if ed.StateKey != nil && *ed.StateKey == "" {
			metadata.NameEvent = ed.Content.Get("name").Str
		}
	case "m.room.avatar":
		if ed.StateKey != nil && *ed.StateKey == "" {
			metadata.AvatarURL = ed.Content.Get("avatar_url").Str
		}
	case "m.room.encryption":
		if ed.StateKey != nil && *ed.StateKey == "" {
			metadata.Encrypted = true
		}
	case "m.room.tombstone":
		if ed.StateKey != nil && *ed.StateKey == "" {
			newRoomID := ed.Content.Get("replacement_room").Str
			if newRoomID == "" {
				metadata.UpgradedRoomID = nil
			} else {
				metadata.UpgradedRoomID = &newRoomID
			}
		}
	case "m.room.canonical_alias":
		if ed.StateKey != nil && *ed.StateKey == "" {
			metadata.CanonicalAlias = ed.Content.Get("alias").Str
		}
	case "m.room.create":
		if ed.StateKey != nil && *ed.StateKey == "" {
			roomType := ed.Content.Get("type")
			if roomType.Exists() && roomType.Type == gjson.String {
				metadata.RoomType = &roomType.Str
			}
			predecessorRoomID := ed.Content.Get("predecessor.room_id").Str
			if predecessorRoomID != "" {
				metadata.PredecessorRoomID = &predecessorRoomID
			}
		}
	case "m.space.child": // only track space child changes for now, not parents
		if ed.StateKey != nil {
			isDeleted := !ed.Content.Get("via").IsArray()
			if isDeleted {
				delete(metadata.ChildSpaceRooms, *ed.StateKey)
			} else {
				metadata.ChildSpaceRooms[*ed.StateKey] = struct{}{}
			}
		}
	case "m.room.member":
		if ed.StateKey != nil {
			membership := ed.Content.Get("membership").Str
			eventJSON := gjson.ParseBytes(ed.Event)
			if internal.IsMembershipChange(eventJSON) {
				metadata.JoinCount = ed.JoinCount
				metadata.InviteCount = ed.InviteCount
				if membership == "leave" || membership == "ban" {
					// remove this user as a hero
					metadata.RemoveHero(*ed.StateKey)
				}

				if membership == "join" && eventJSON.Get("unsigned.prev_content.membership").Str == "invite" {
					// invite -> join, retire any outstanding invites
					err := c.store.InvitesTable.RemoveInvite(*ed.StateKey, ed.RoomID)
					if err != nil {
						logger.Err(err).Str("user", *ed.StateKey).Str("room", ed.RoomID).Msg("failed to remove accepted invite")
						internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
					}
				}
			}
			if len(metadata.Heroes) < 6 && (membership == "join" || membership == "invite") {
				// try to find the existing hero e.g they changed their display name
				found := false
				for i := range metadata.Heroes {
					if metadata.Heroes[i].ID == *ed.StateKey {
						metadata.Heroes[i].Name = ed.Content.Get("displayname").Str
						found = true
						break
					}
				}
				if !found {
					metadata.Heroes = append(metadata.Heroes, internal.Hero{
						ID:   *ed.StateKey,
						Name: ed.Content.Get("displayname").Str,
					})
				}
			}
		}
	}
	// Note: this means the LastMessageTimestamp and values in LatestEventsByType can
	// _decrease_; these timestamps are not monotonic.
	metadata.LastMessageTimestamp = ed.Timestamp
	metadata.LatestEventsByType[ed.EventType] = internal.EventMetadata{
		NID:       ed.NID,
		Timestamp: ed.Timestamp,
	}
	c.roomIDToMetadata[ed.RoomID] = metadata
}
