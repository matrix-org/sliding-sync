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
}

// ConnMap stores a collection of Conns along with other global server-wide state e.g the in-memory
// map of which users are joined to which rooms.
type ConnMap struct {
	cache *ttlcache.Cache

	// map of user_id to active connections. Inspect the ConnID to find the device ID.
	userIDToConn map[string][]*Conn
	connIDToConn map[string]*Conn

	// global room trackers (not connection or user specific)
	jrt            *JoinedRoomsTracker
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
func (m *ConnMap) GetOrCreateConn(cid ConnID, userID string) *Conn {
	// atomically check if a conn exists already and return that if so
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := m.Conn(cid)
	if conn != nil {
		return conn
	}
	state := NewConnState(userID, m.store)
	conn = NewConn(cid, state, state.HandleIncomingRequest)
	m.cache.Set(cid.String(), conn)
	m.connIDToConn[cid.String()] = conn
	m.userIDToConn[userID] = append(m.userIDToConn[userID], conn)
	return conn
}

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
func (m *ConnMap) OnNewEvent(
	roomID string, event json.RawMessage,
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

	ed := &EventData{
		event:     event,
		roomID:    roomID,
		eventType: eventType,
		stateKey:  stateKey,
		content:   ev.Get("content"),
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
