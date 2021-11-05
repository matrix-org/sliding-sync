package sync3

import (
	"sync"
	"time"

	"github.com/ReneKroon/ttlcache/v2"
)

// ConnMap stores a collection of Conns along with other global server-wide state e.g the in-memory
// map of which users are joined to which rooms.
type ConnMap struct {
	cache *ttlcache.Cache

	// map of user_id to active connections. Inspect the ConnID to find the device ID.
	userIDToConn map[string][]*Conn
	connIDToConn map[string]*Conn

	mu *sync.Mutex
}

func NewConnMap() *ConnMap {
	cm := &ConnMap{
		userIDToConn: make(map[string][]*Conn),
		connIDToConn: make(map[string]*Conn),
		cache:        ttlcache.NewCache(),
		mu:           &sync.Mutex{},
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

// Atomically gets or creates a connection with this connection ID. Calls newConn if a new connection is required.
func (m *ConnMap) GetOrCreateConn(cid ConnID, newConnHandler func() ConnHandler) (*Conn, bool) {
	// atomically check if a conn exists already and return that if so
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := m.Conn(cid)
	if conn != nil {
		return conn, false
	}
	h := newConnHandler()
	conn = NewConn(cid, h)
	m.cache.Set(cid.String(), conn)
	m.connIDToConn[cid.String()] = conn
	m.userIDToConn[h.UserID()] = append(m.userIDToConn[h.UserID()], conn)
	return conn, true
}

func (m *ConnMap) closeConn(connID string, value interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// remove conn from all the maps
	conn := value.(*Conn)
	delete(m.connIDToConn, connID)
	h := conn.handler
	conns := m.userIDToConn[h.UserID()]
	for i := 0; i < len(conns); i++ {
		if conns[i].ConnID.String() == connID {
			// delete without preserving order
			conns[i] = conns[len(conns)-1]
			conns = conns[:len(conns)-1]
		}
	}
	m.userIDToConn[h.UserID()] = conns
	h.Destroy()
}
