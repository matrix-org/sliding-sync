package synclive

import (
	"context"
	"sync"
	"time"

	"github.com/ReneKroon/ttlcache/v2"
	"github.com/matrix-org/sync-v3/state"
	"github.com/tidwall/gjson"
)

type Notifier struct {
	cache *ttlcache.Cache

	// map of user_id to active connections. Inspect the ConnID to find the device ID.
	userIDToConn map[string][]*Conn
	connIDToConn map[string]*Conn
	connIDToUser map[string]string

	jrt   *JoinedRoomsTracker
	store *state.Storage

	mu *sync.Mutex
}

func NewNotifier(store *state.Storage) *Notifier {
	cm := &Notifier{
		userIDToConn: make(map[string][]*Conn),
		connIDToConn: make(map[string]*Conn),
		connIDToUser: make(map[string]string),
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
func (m *Notifier) Conn(cid ConnID) *Conn {
	cint, _ := m.cache.Get(cid.String())
	if cint == nil {
		return nil
	}
	return cint.(*Conn)
}

// Atomically gets or creates a connection with this connection ID.
func (m *Notifier) GetOrCreateConn(cid ConnID, userID string) *Conn {
	// atomically check if a conn exists already and return that if so
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := m.Conn(cid)
	if conn != nil {
		return conn
	}
	conn = NewConn(cid, m.handleIncomingSyncRequest)
	m.cache.Set(cid.String(), conn)
	m.connIDToConn[cid.String()] = conn
	m.userIDToConn[userID] = append(m.userIDToConn[userID], conn)
	m.connIDToUser[cid.String()] = userID
	return conn
}

func (m *Notifier) LoadJoinedUsers(roomIDToUserIDs map[string][]string) {
	for roomID, userIDs := range roomIDToUserIDs {
		for _, userID := range userIDs {
			m.jrt.UserJoinedRoom(userID, roomID)
		}
	}
}

func (m *Notifier) closeConn(connID string, _ interface{}) {
	// remove conn from all the maps
	delete(m.connIDToConn, connID)
	userID := m.connIDToUser[connID]
	delete(m.connIDToUser, connID)
	if userID != "" {
		conns := m.userIDToConn[userID]
		for i := 0; i < len(conns); i++ {
			if conns[i].ConnID.String() == connID {
				// delete without preserving order
				conns[i] = conns[len(conns)-1]
				conns = conns[:len(conns)-1]
			}
		}
		m.userIDToConn[userID] = conns
	}
}

// Implements Conn.HandleIncomingRequest
func (m *Notifier) handleIncomingSyncRequest(ctx context.Context, connID ConnID, reqBody []byte) ([]byte, error) {
	// pull sticky request data from ConnID, mux with new reqBody to form complete sync request.

	// update room subscriptions

	// check if the ranges have changed. If there are new ranges, track them and send them back

	return reqBody, nil // echobot
}

// Call this when there is a new event received on a v2 stream.
// This event must be globally unique, i.e indicated so by the state store.
func (m *Notifier) OnNewEvent(
	roomID, sender, eventType string, stateKey *string, content gjson.Result,
) {
	// de-duplicate the conns

	// check if conn isn't blocking this event in their filter / ranges

	// send message
}

// Load the current state of rooms from storage based on the request parameters
func LoadRooms(s *state.Storage, req *Request, roomIDs []string) (map[string]Room, error) {
	return nil, nil
}
