package sync3

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/state"
	"github.com/tidwall/gjson"
)

type GlobalCache struct {
	LoadJoinedRoomsOverride func(userID string) (pos int64, joinedRooms []internal.RoomMetadata, err error)

	// inserts are done by v2 poll loops, selects are done by v3 request threads
	// there are lots of overlapping keys as many users (threads) can be joined to the same room (key)
	// hence you must lock this with `mu` before r/w
	roomIDToMetadata   map[string]*internal.RoomMetadata
	roomIDToMetadataMu *sync.RWMutex

	// for loading room state not held in-memory
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

func (c *GlobalCache) LoadRoom(roomID string) *internal.RoomMetadata {
	c.roomIDToMetadataMu.RLock()
	defer c.roomIDToMetadataMu.RUnlock()
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
	return &srCopy
}

func (c *GlobalCache) AssignRoom(r internal.RoomMetadata) {
	c.roomIDToMetadataMu.Lock()
	defer c.roomIDToMetadataMu.Unlock()
	c.roomIDToMetadata[r.RoomID] = &r
}

func (c *GlobalCache) LoadJoinedRooms(userID string) (pos int64, joinedRooms []internal.RoomMetadata, err error) {
	if c.LoadJoinedRoomsOverride != nil {
		return c.LoadJoinedRoomsOverride(userID)
	}
	initialLoadPosition, err := c.store.LatestEventNID()
	if err != nil {
		return 0, nil, err
	}
	joinedRoomIDs, err := c.store.JoinedRoomsAfterPosition(userID, initialLoadPosition)
	if err != nil {
		return 0, nil, err
	}
	rooms := make([]internal.RoomMetadata, len(joinedRoomIDs))
	for i, roomID := range joinedRoomIDs {
		rooms[i] = *c.LoadRoom(roomID)
	}
	return initialLoadPosition, rooms, nil
}

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
	for _, rs := range requiredState {
		eventTypeSet[rs[0]] = true
		requiredStateMap[rs[0]] = append(requiredStateMap[rs[0]], rs[1])
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
	for roomID, metadata := range roomIDToMetadata {
		fmt.Printf("Room: %s - %s - %s \n", roomID, metadata.NameEvent, gomatrixserverlib.Timestamp(metadata.LastMessageTimestamp).Time())
		c.AssignRoom(metadata)
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
	metadata := c.roomIDToMetadata[ed.roomID]
	if metadata == nil {
		metadata = &internal.RoomMetadata{
			RoomID: ed.roomID,
		}
	}
	switch ed.eventType {
	case "m.room.name":
		if ed.stateKey != nil && *ed.stateKey == "" {
			metadata.NameEvent = ed.content.Get("name").Str
		}
	case "m.room.canonical_alias":
		if ed.stateKey != nil && *ed.stateKey == "" {
			metadata.CanonicalAlias = ed.content.Get("alias").Str
		}
	case "m.room.member":
		if ed.stateKey != nil {
			membership := ed.content.Get("membership").Str
			if membership == "invite" {
				metadata.InviteCount += 1
			} else if membership == "join" {
				metadata.JoinCount += 1
			} else if membership == "leave" || membership == "ban" {
				metadata.JoinCount -= 1
				// remove this user as a hero
				metadata.RemoveHero(*ed.stateKey)
			}
			if gjson.ParseBytes(ed.event).Get("unsigned.prev_content.membership").Str == "invite" {
				metadata.InviteCount -= 1
			}
			if len(metadata.Heroes) < 6 && (membership == "join" || membership == "invite") {
				metadata.Heroes = append(metadata.Heroes, internal.Hero{
					ID:   *ed.stateKey,
					Name: ed.content.Get("displayname").Str,
				})
			}
		}
	}
	metadata.LastMessageTimestamp = ed.timestamp
	c.roomIDToMetadata[ed.roomID] = metadata
}
