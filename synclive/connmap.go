package synclive

import (
	"encoding/json"
	"fmt"
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

	// TODO: this can be pulled out of here and invoked from handler?
	globalRoomInfo map[string]*SortableRoom

	store *state.Storage

	mu *sync.Mutex
}

func NewConnMap(store *state.Storage) *ConnMap {
	cm := &ConnMap{
		userIDToConn:   make(map[string][]*Conn),
		connIDToConn:   make(map[string]*Conn),
		cache:          ttlcache.NewCache(),
		mu:             &sync.Mutex{},
		jrt:            NewJoinedRoomsTracker(),
		store:          store,
		globalRoomInfo: make(map[string]*SortableRoom),
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
func (m *ConnMap) GetOrCreateConn(cid ConnID, userID string) (*Conn, bool) {
	// atomically check if a conn exists already and return that if so
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := m.Conn(cid)
	if conn != nil {
		return conn, false
	}
	state := NewConnState(userID, m.store, m.roomInfo)
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
func (m *ConnMap) LoadBaseline(roomIDToUserIDs map[string][]string) error {
	latest, err := m.store.LatestEventNID()
	if err != nil {
		return err
	}
	for roomID, userIDs := range roomIDToUserIDs {
		room := &SortableRoom{
			RoomID: roomID,
		}
		// load current state for room
		stateEvents, err := m.store.RoomStateAfterEventPosition(roomID, latest)
		if err != nil {
			return err
		}
		for _, ev := range stateEvents {
			// TODO: name algorithm
			if ev.Type == "m.room.name" {
				room.Name = gjson.ParseBytes(ev.JSON).Get("content.name").Str
				break
			}
		}
		latest, err := m.store.LatestEventInRoom(roomID, latest)
		if err != nil {
			return err
		}
		room.LastMessageTimestamp = gjson.ParseBytes(latest.JSON).Get("origin_server_ts").Int()
		m.globalRoomInfo[room.RoomID] = room
		for _, userID := range userIDs {
			m.jrt.UserJoinedRoom(userID, roomID)
		}
		fmt.Printf("Room: %+v \n", room)
	}
	return nil
}

func (m *ConnMap) roomInfo(roomID string) *SortableRoom {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.globalRoomInfo[roomID]
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

// Call this when there is a new event received on a v2 stream.
// This event must be globally unique, i.e indicated so by the state store.
func (m *ConnMap) OnNewEvents(
	roomID string, events []json.RawMessage, latestPos int64,
) {
	for _, event := range events {
		m.onNewEvent(roomID, event, latestPos)
	}
}
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
	// update global state
	m.mu.Lock()
	globalRoom := m.globalRoomInfo[roomID]
	if globalRoom == nil {
		globalRoom = &SortableRoom{
			RoomID: roomID,
		}
	}
	if eventType == "m.room.name" && stateKey != nil && *stateKey == "" {
		globalRoom.Name = gjson.ParseBytes(event).Get("content.name").Str
	}
	globalRoom.LastMessageTimestamp = gjson.ParseBytes(event).Get("origin_server_ts").Int()
	m.globalRoomInfo[globalRoom.RoomID] = globalRoom
	m.mu.Unlock()

	ed := &EventData{
		event:     event,
		roomID:    roomID,
		eventType: eventType,
		stateKey:  stateKey,
		content:   ev.Get("content"),
		latestPos: latestPos,
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
