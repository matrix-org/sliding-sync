package observables

import "sync"

type ConnID struct {
	SessionID string
	DeviceID  string
}

type Conn struct {
	ConnID ConnID
	// The position in the stream last sent to the client
	ServerPosition int64
	// The position in the stream last confirmed by the client
	ClientPosition int64
}

type ConnMap struct {
	mu   *sync.Mutex
	cmap map[ConnID]*Conn
}

func (m *ConnMap) Conn(cid ConnID) *Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmap[cid]
}

func (m *ConnMap) SetConn(conn *Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cmap[conn.ConnID] = conn
}
