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
	jrt          *JoinedRoomsTracker

	mu *sync.Mutex
}

func NewNotifier() *Notifier {
	cm := &Notifier{
		userIDToConn: make(map[string][]*Conn),
		cache:        ttlcache.NewCache(),
		mu:           &sync.Mutex{},
		jrt:          NewJoinedRoomsTracker(),
	}
	cm.cache.SetTTL(30 * time.Minute) // TODO: customisable
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
func (m *Notifier) GetOrCreateConn(cid ConnID) *Conn {
	// atomically check if a conn exists already and return that if so
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := m.Conn(cid)
	if conn != nil {
		return conn
	}
	conn = NewConn(cid, m.handleIncomingSyncRequest)
	m.cache.Set(conn.ConnID.String(), conn)
	return conn
}

func (m *Notifier) LoadJoinedUsers(roomIDToUserIDs map[string][]string) {
	for roomID, userIDs := range roomIDToUserIDs {
		for _, userID := range userIDs {
			m.jrt.UserJoinedRoom(userID, roomID)
		}
	}
}

// Implements Conn.HandleIncomingRequest
func (m *Notifier) handleIncomingSyncRequest(ctx context.Context, connID ConnID, reqBody []byte) ([]byte, error) {
	// mux together the reqBody to see if the request has fundamentally changed.
	// the request has changed, invalidate what we have and return fresh.
	// the requre hasn't changed,
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
