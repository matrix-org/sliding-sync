package sync3

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/state"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

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
	LoadJoinedRoomsOverride func(userID string) (pos int64, joinedRooms []*internal.RoomMetadata, err error)

	// inserts are done by v2 poll loops, selects are done by v3 request threads
	// there are lots of overlapping keys as many users (threads) can be joined to the same room (key)
	// hence you must lock this with `mu` before r/w
	roomIDToMetadata   map[string]*internal.RoomMetadata
	roomIDToMetadataMu *sync.RWMutex

	// for loading room state not held in-memory TODO: remove to another struct along with associated functions
	store *state.Storage

	id int
}

func NewGlobalCache(store *state.Storage) *GlobalCache {
	return &GlobalCache{
		roomIDToMetadataMu: &sync.RWMutex{},
		store:              store,
		roomIDToMetadata:   make(map[string]*internal.RoomMetadata),
	}
}

// Load the current room metadata for the given room IDs. Races unless you call this in a dispatcher loop.
// Always returns copies of the room metadata so ownership can be passed to other threads.
// Keeps the ordering of the room IDs given.
func (c *GlobalCache) LoadRooms(roomIDs ...string) []*internal.RoomMetadata {
	c.roomIDToMetadataMu.RLock()
	defer c.roomIDToMetadataMu.RUnlock()
	result := make([]*internal.RoomMetadata, len(roomIDs))
	for i := range result {
		roomID := roomIDs[i]
		sr := c.roomIDToMetadata[roomID]
		if sr == nil {
			logger.Error().Str("room", roomID).Msg("GlobalCache.LoadRoom: no metadata for this room")
			return nil
		}
		srCopy := *sr
		// copy the heroes or else we may modify the same slice which would be bad :(
		srCopy.Heroes = make([]internal.Hero, len(sr.Heroes))
		for i := range sr.Heroes {
			srCopy.Heroes[i] = sr.Heroes[i]
		}
		result[i] = &srCopy
	}
	return result
}

// Load all current joined room metadata for the user given. Returns the absolute database position along
// with the results. TODO: remove with LoadRoomState?
func (c *GlobalCache) LoadInvitedJoinedRooms(userID string) (pos int64, joinedRooms []*internal.RoomMetadata, err error) {
	if c.LoadJoinedRoomsOverride != nil {
		return c.LoadJoinedRoomsOverride(userID)
	}
	initialLoadPosition, err := c.store.LatestEventNID()
	if err != nil {
		return 0, nil, err
	}
	joinedRoomIDs, err := c.store.JoinedRoomsAfterPosition(userID, initialLoadPosition, true)
	if err != nil {
		return 0, nil, err
	}
	// TODO: no guarantee that this state is the same as latest unless called in a dispatcher loop
	rooms := c.LoadRooms(joinedRoomIDs...)

	return initialLoadPosition, rooms, nil
}

// TODO: remove? Doesn't touch global cache fields
func (c *GlobalCache) LoadRoomState(roomID string, loadPosition int64, requiredState [][2]string) []json.RawMessage {
	if len(requiredState) == 0 {
		return nil
	}
	if c.store == nil {
		return nil
	}
	// pull out unique event types and convert the required state into a map
	eventTypeSet := make(map[string]bool)
	requiredStateMap := make(map[string][]string) // event_type -> []state_key
	hasWildcardEventType := false
	for _, rs := range requiredState {
		if rs[0] == "*" {
			hasWildcardEventType = true
		}
		eventTypeSet[rs[0]] = true
		requiredStateMap[rs[0]] = append(requiredStateMap[rs[0]], rs[1])
	}
	// if we have wildcard event types we need to pull all room state and cannot only pull out certain
	// event types
	if hasWildcardEventType {
		eventTypeSet = make(map[string]bool)
	}
	eventTypes := make([]string, len(eventTypeSet))
	i := 0
	for et := range eventTypeSet {
		eventTypes[i] = et
		i++
	}
	stateEvents, err := c.store.RoomStateAfterEventPosition(roomID, loadPosition, eventTypes...)
	if err != nil {
		logger.Err(err).Str("room", roomID).Int64("pos", loadPosition).Msg("failed to load room state")
		return nil
	}
	var result []json.RawMessage
	for _, ev := range stateEvents {
		stateKeys := requiredStateMap[ev.Type]
		stateKeys = append(stateKeys, requiredStateMap["*"]...)
		include := false
		for _, sk := range stateKeys {
			if sk == "*" { // wildcard
				include = true
				break
			}
			if sk == ev.StateKey {
				include = true
				break
			}
		}
		if include {
			result = append(result, ev.JSON)
		}
	}
	// TODO: cache?
	return result
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
	for roomID := range roomIDToMetadata {
		metadata := roomIDToMetadata[roomID]
		fmt.Printf("Room: %s - %s - %s \n", roomID, metadata.NameEvent, gomatrixserverlib.Timestamp(metadata.LastMessageTimestamp).Time())
		internal.Assert("room ID is set", metadata.RoomID != "")
		internal.Assert("last message timestamp exists", metadata.LastMessageTimestamp > 1)
		c.roomIDToMetadata[roomID] = &metadata
	}
	return nil
}

// =================================================
// Listener function called by dispatcher below
// =================================================

func (c *GlobalCache) OnNewEvent(
	ed *EventData,
) {
	// update global state
	c.roomIDToMetadataMu.Lock()
	defer c.roomIDToMetadataMu.Unlock()
	metadata := c.roomIDToMetadata[ed.RoomID]
	if metadata == nil {
		metadata = &internal.RoomMetadata{
			RoomID: ed.RoomID,
		}
	}
	switch ed.EventType {
	case "m.room.name":
		if ed.StateKey != nil && *ed.StateKey == "" {
			metadata.NameEvent = ed.Content.Get("name").Str
		}
	case "m.room.encryption":
		if ed.StateKey != nil && *ed.StateKey == "" {
			metadata.Encrypted = true
		}
	case "m.room.canonical_alias":
		if ed.StateKey != nil && *ed.StateKey == "" {
			metadata.CanonicalAlias = ed.Content.Get("alias").Str
		}
	case "m.room.member":
		if ed.StateKey != nil {
			membership := ed.Content.Get("membership").Str
			eventJSON := gjson.ParseBytes(ed.Event)
			if internal.IsMembershipChange(eventJSON) {
				if membership == "invite" {
					metadata.InviteCount += 1
				} else if membership == "join" {
					metadata.JoinCount += 1
				} else if membership == "leave" || membership == "ban" {
					metadata.JoinCount -= 1
					// remove this user as a hero
					metadata.RemoveHero(*ed.StateKey)
				}

				if eventJSON.Get("unsigned.prev_content.membership").Str == "invite" {
					metadata.InviteCount -= 1
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
	metadata.LastMessageTimestamp = ed.Timestamp
	c.roomIDToMetadata[ed.RoomID] = metadata
}
