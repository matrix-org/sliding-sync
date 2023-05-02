package sync3

import (
	"sync"
	"time"

	"github.com/ReneKroon/ttlcache/v2"
)

// ConnMap stores a collection of Conns.
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
	cm.cache.SetExpirationCallback(cm.closeConnExpires)
	return cm
}

func (m *ConnMap) Teardown() {
	m.cache.Close()
}

func (m *ConnMap) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.connIDToConn)
}

// Conn returns a connection with this ConnID. Returns nil if no connection exists.
func (m *ConnMap) Conn(cid ConnID) *Conn {
	cint, _ := m.cache.Get(cid.String())
	if cint == nil {
		return nil
	}
	conn := cint.(*Conn)
	if conn.Alive() {
		return conn
	}
	// e.g buffer exceeded, close it and remove it from the cache
	logger.Trace().Str("conn", cid.String()).Msg("closing connection due to dead connection (buffer full)")
	m.closeConn(conn)
	return nil
}

// Atomically gets or creates a connection with this connection ID. Calls newConn if a new connection is required.
func (m *ConnMap) CreateConn(cid ConnID, newConnHandler func() ConnHandler) (*Conn, bool) {
	// atomically check if a conn exists already and nuke it if it exists
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := m.Conn(cid)
	if conn != nil {
		// tear down this connection and fallthrough
		logger.Trace().Str("conn", cid.String()).Msg("closing connection due to CreateConn called again")
		m.closeConn(conn)
	}
	h := newConnHandler()
	conn = NewConn(cid, h)
	m.cache.Set(cid.String(), conn)
	m.connIDToConn[cid.String()] = conn
	m.userIDToConn[cid.UserID] = append(m.userIDToConn[cid.UserID], conn)
	return conn, true
}

func (m *ConnMap) CloseConn(connID ConnID) {
	logger.Trace().Str("conn", connID.String()).Msg("closing connection due to CloseConn()")
	m.cache.Remove(connID.String()) // this will fire TTL callbacks which calls closeConn
}

func (m *ConnMap) closeConnExpires(connID string, value interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := value.(*Conn)
	logger.Trace().Str("conn", connID).Msg("closing connection due to expired TTL in cache")
	m.closeConn(conn)
}

// must hold mu
func (m *ConnMap) closeConn(conn *Conn) {
	if conn == nil {
		return
	}

	connKey := conn.ConnID.String()
	logger.Trace().Str("conn", connKey).Msg("closing connection")
	// remove conn from all the maps
	delete(m.connIDToConn, connKey)
	h := conn.handler
	conns := m.userIDToConn[conn.UserID]
	for i := 0; i < len(conns); i++ {
		if conns[i].DeviceID == conn.DeviceID {
			// delete without preserving order
			conns[i] = conns[len(conns)-1]
			conns = conns[:len(conns)-1]
		}
	}
	m.userIDToConn[conn.UserID] = conns
	// remove user cache listeners etc
	h.Destroy()
}
