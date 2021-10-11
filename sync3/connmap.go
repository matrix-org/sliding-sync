package sync3

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/ReneKroon/ttlcache/v2"
	"github.com/matrix-org/sync-v3/state"
	"github.com/tidwall/gjson"
)

type EventData struct {
	event     json.RawMessage
	roomID    string
	eventType string
	stateKey  *string
	content   gjson.Result
	timestamp int64

	// TODO: remove or factor out
	userRoomData *UserRoomData
	// the absolute latest position for this event data. The NID for this event is guaranteed to
	// be <= this value.
	latestPos int64
}

// ConnMap stores a collection of Conns along with other global server-wide state e.g the in-memory
// map of which users are joined to which rooms.
type ConnMap struct {
	cache *ttlcache.Cache

	// map of user_id to active connections. Inspect the ConnID to find the device ID.
	userIDToConn map[string][]*Conn
	connIDToConn map[string]*Conn

	// global room trackers (not connection or user specific)
	// The joined room tracker must be loaded with the current joined room state for all users
	// BEFORE v2 poll loops are started, else it could race with live updates.
	jrt *JoinedRoomsTracker

	mu *sync.Mutex

	store *state.Storage
}

func NewConnMap(store *state.Storage) *ConnMap {
	cm := &ConnMap{
		userIDToConn: make(map[string][]*Conn),
		connIDToConn: make(map[string]*Conn),
		cache:        ttlcache.NewCache(),
		mu:           &sync.Mutex{},
		jrt:          NewJoinedRoomsTracker(),
		store:        store,
	}
	cm.cache.SetTTL(30 * time.Minute) // TODO: customisable
	cm.cache.SetExpirationCallback(cm.closeConn)
	return cm
}

// Conn returns a connection with this ConnID. Returns nil if no connection exists.
func (m *ConnMap) Conn(cid ConnID) *Conn {
	cint, _ := m.cache.Get(cid.String())
	if cint == nil {
		return nil
	}
	return cint.(*Conn)
}

// Atomically gets or creates a connection with this connection ID.
func (m *ConnMap) GetOrCreateConn(cid ConnID, globalCache *GlobalCache, userID string, userCache *UserCache) (*Conn, bool) {
	// atomically check if a conn exists already and return that if so
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := m.Conn(cid)
	if conn != nil {
		return conn, false
	}
	state := NewConnState(userID, userCache, globalCache, m)
	conn = NewConn(cid, state, state.HandleIncomingRequest)
	m.cache.Set(cid.String(), conn)
	m.connIDToConn[cid.String()] = conn
	m.userIDToConn[userID] = append(m.userIDToConn[userID], conn)
	return conn, true
}

// LoadBaseline must be called before any v2 poll loops are made. Failure to do so can result in
// duplicate event processing which could corrupt state. Consider:
//   - V2 poll loop started early
//   - Join event arrives, NID=50
//   - LoadBaseline loads the latest NID=50 due to LatestEventNID, processes this join event in the process
//   - OnNewEvents is called with the join event
//   - join event is processed twice.
// TODO: Move to cache struct
func (m *ConnMap) LoadBaseline(roomIDToUserIDs map[string][]string) error {
	// loop all joined rooms, some of which may not be present in globalRoomInfo if they have no state
	for roomID, userIDs := range roomIDToUserIDs {
		for _, userID := range userIDs {
			m.jrt.UserJoinedRoom(userID, roomID)
		}
	}
	return nil
}

func (m *ConnMap) LoadState(roomID string, loadPosition int64, requiredState [][2]string) []json.RawMessage {
	if len(requiredState) == 0 {
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
	stateEvents, err := m.store.RoomStateAfterEventPosition(roomID, loadPosition, eventTypes...)
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

func (m *ConnMap) Load(userID string) (joinedRoomIDs []string, initialLoadPosition int64, err error) {
	initialLoadPosition, err = m.store.LatestEventNID()
	if err != nil {
		return
	}
	joinedRoomIDs, err = m.store.JoinedRoomsAfterPosition(userID, initialLoadPosition)
	return
}

func (m *ConnMap) closeConn(connID string, value interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// remove conn from all the maps
	conn := value.(*Conn)
	delete(m.connIDToConn, connID)
	state := conn.connState
	if state != nil {
		conns := m.userIDToConn[state.UserID()]
		for i := 0; i < len(conns); i++ {
			if conns[i].ConnID.String() == connID {
				// delete without preserving order
				conns[i] = conns[len(conns)-1]
				conns = conns[:len(conns)-1]
			}
		}
		m.userIDToConn[state.UserID()] = conns
	}
}

// TODO: Move to cache struct
// Call this when there is a new event received on a v2 stream.
// This event must be globally unique, i.e indicated so by the state store.
func (m *ConnMap) OnNewEvents(
	roomID string, events []json.RawMessage, latestPos int64,
) {
	for _, event := range events {
		m.onNewEvent(roomID, event, latestPos)
	}
}

// TODO: Move to cache struct
func (m *ConnMap) onNewEvent(
	roomID string, event json.RawMessage, latestPos int64,
) {
	// parse the event to pull out fields we care about
	var stateKey *string
	ev := gjson.ParseBytes(event)
	if sk := ev.Get("state_key"); sk.Exists() {
		stateKey = &sk.Str
	}
	eventType := ev.Get("type").Str

	// update the tracker
	targetUser := ""
	if eventType == "m.room.member" && stateKey != nil {
		targetUser = *stateKey
		// TODO: de-dupe joins in jrt else profile changes will results in 2x room IDs
		membership := ev.Get("content.membership").Str
		switch membership {
		case "join":
			m.jrt.UserJoinedRoom(targetUser, roomID)
		case "ban":
			fallthrough
		case "leave":
			m.jrt.UserLeftRoom(targetUser, roomID)
		}
	}
	eventTimestamp := ev.Get("origin_server_ts").Int()

	ed := &EventData{
		event:     event,
		roomID:    roomID,
		eventType: eventType,
		stateKey:  stateKey,
		content:   ev.Get("content"),
		latestPos: latestPos,
		timestamp: eventTimestamp,
	}

	// notify all people in this room
	notifiedTargetUser := false
	userIDs := m.jrt.JoinedUsersForRoom(roomID)
	for _, userID := range userIDs {
		m.mu.Lock()
		conns := m.userIDToConn[userID]
		m.mu.Unlock()
		for _, conn := range conns {
			conn.PushNewEvent(ed)
			if userID == targetUser {
				notifiedTargetUser = true
			}
		}
	}
	if !notifiedTargetUser {
		m.mu.Lock()
		conns := m.userIDToConn[targetUser]
		m.mu.Unlock()
		for _, conn := range conns {
			conn.PushNewEvent(ed)
		}
	}
}
