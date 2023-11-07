package sync3

import (
	"context"
	"sync"
	"time"

	"github.com/ReneKroon/ttlcache/v2"
	"github.com/prometheus/client_golang/prometheus"
)

// ConnMap stores a collection of Conns.
type ConnMap struct {
	cache *ttlcache.Cache

	// map of user_id to active connections. Inspect the ConnID to find the device ID.
	userIDToConn map[string][]*Conn
	connIDToConn map[string]*Conn

	numConns prometheus.Gauge
	// counters for reasons why connections have expired
	expiryTimedOutCounter   prometheus.Counter
	expiryBufferFullCounter prometheus.Counter

	mu *sync.Mutex
}

func NewConnMap(enablePrometheus bool) *ConnMap {
	cm := &ConnMap{
		userIDToConn: make(map[string][]*Conn),
		connIDToConn: make(map[string]*Conn),
		cache:        ttlcache.NewCache(),
		mu:           &sync.Mutex{},
	}
	cm.cache.SetTTL(30 * time.Minute) // TODO: customisable
	cm.cache.SetExpirationCallback(cm.closeConnExpires)

	if enablePrometheus {
		cm.expiryTimedOutCounter = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sliding_sync",
			Subsystem: "api",
			Name:      "expiry_conn_timed_out",
			Help:      "Counter of expired API connections due to reaching TTL limit",
		})
		prometheus.MustRegister(cm.expiryTimedOutCounter)
		cm.expiryBufferFullCounter = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sliding_sync",
			Subsystem: "api",
			Name:      "expiry_conn_buffer_full",
			Help:      "Counter of expired API connections due to reaching buffer update limit",
		})
		prometheus.MustRegister(cm.expiryBufferFullCounter)
		cm.numConns = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sliding_sync",
			Subsystem: "api",
			Name:      "num_active_conns",
			Help:      "Number of active sliding sync connections.",
		})
		prometheus.MustRegister(cm.numConns)
	}
	return cm
}

func (m *ConnMap) Teardown() {
	m.cache.Close()

	if m.numConns != nil {
		prometheus.Unregister(m.numConns)
	}
	if m.expiryBufferFullCounter != nil {
		prometheus.Unregister(m.expiryBufferFullCounter)
	}
	if m.expiryTimedOutCounter != nil {
		prometheus.Unregister(m.expiryTimedOutCounter)
	}
}

// UpdateMetrics recalculates the number of active connections. Do this when you think there is a change.
func (m *ConnMap) UpdateMetrics() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateMetrics(len(m.connIDToConn))
}

// updateMetrics is like UpdateMetrics but doesn't touch connIDToConn and hence doesn't need to lock. We use this internally
// when we need to update the metric and already have the lock held, as calling UpdateMetrics would deadlock.
func (m *ConnMap) updateMetrics(numConns int) {
	if m.numConns == nil {
		return
	}
	m.numConns.Set(float64(numConns))
}

// Conns return all connections for this user|device
func (m *ConnMap) Conns(userID, deviceID string) []*Conn {
	connIDs := m.connIDsForDevice(userID, deviceID)
	var conns []*Conn
	for _, connID := range connIDs {
		c := m.Conn(connID)
		if c != nil {
			conns = append(conns, c)
		}
	}
	return conns
}

// Conn returns a connection with this ConnID. Returns nil if no connection exists.
func (m *ConnMap) Conn(cid ConnID) *Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getConn(cid)
}

// getConn returns a connection with this ConnID. Returns nil if no connection exists. Expires connections if the buffer is full.
// Must hold mu.
func (m *ConnMap) getConn(cid ConnID) *Conn {
	cint, _ := m.cache.Get(cid.String())
	if cint == nil {
		return nil
	}
	conn := cint.(*Conn)
	if conn.Alive() {
		return conn
	}
	// e.g buffer exceeded, close it and remove it from the cache
	logger.Info().Str("conn", cid.String()).Msg("closing connection due to dead connection (buffer full)")
	m.closeConn(conn)
	if m.expiryBufferFullCounter != nil {
		m.expiryBufferFullCounter.Inc()
	}
	return nil
}

// Atomically gets or creates a connection with this connection ID. Calls newConn if a new connection is required.
func (m *ConnMap) CreateConn(cid ConnID, cancel context.CancelFunc, newConnHandler func() ConnHandler) (*Conn, bool) {
	// atomically check if a conn exists already and nuke it if it exists
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := m.getConn(cid)
	if conn != nil {
		// tear down this connection and fallthrough
		isSpamming := conn.lastPos <= 1
		if isSpamming {
			// the existing connection has only just been used for one response, and now they are asking
			// for a new connection. Apply an artificial delay here to stop buggy clients from spamming
			// /sync without a `?pos=` value.
			time.Sleep(SpamProtectionInterval)
		}
		logger.Trace().Str("conn", cid.String()).Bool("spamming", isSpamming).Msg("closing connection due to CreateConn called again")
		m.closeConn(conn)
	}
	h := newConnHandler()
	h.SetCancelCallback(cancel)
	conn = NewConn(cid, h)
	m.cache.Set(cid.String(), conn)
	m.connIDToConn[cid.String()] = conn
	m.userIDToConn[cid.UserID] = append(m.userIDToConn[cid.UserID], conn)
	m.updateMetrics(len(m.connIDToConn))
	return conn, true
}

func (m *ConnMap) CloseConnsForDevice(userID, deviceID string) {
	logger.Trace().Str("user", userID).Str("device", deviceID).Msg("closing connections due to CloseConn()")
	// gather open connections for this user|device
	connIDs := m.connIDsForDevice(userID, deviceID)
	for _, cid := range connIDs {
		m.cache.Remove(cid.String()) // this will fire TTL callbacks which calls closeConn
	}
}

func (m *ConnMap) connIDsForDevice(userID, deviceID string) []ConnID {
	m.mu.Lock()
	defer m.mu.Unlock()
	var connIDs []ConnID
	conns := m.userIDToConn[userID]
	for _, c := range conns {
		if c.DeviceID == deviceID {
			connIDs = append(connIDs, c.ConnID)
		}
	}
	return connIDs
}

// CloseConnsForUsers closes all conns for a given slice of users. Returns the number of
// conns closed.
func (m *ConnMap) CloseConnsForUsers(userIDs []string) (closed int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, userID := range userIDs {
		conns := m.userIDToConn[userID]
		logger.Trace().Str("user", userID).Int("num_conns", len(conns)).Msg("closing all device connections due to CloseConn()")

		for _, conn := range conns {
			m.cache.Remove(conn.String()) // this will fire TTL callbacks which calls closeConn
		}
		closed += len(conns)
	}
	return closed
}

func (m *ConnMap) closeConnExpires(connID string, value interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := value.(*Conn)
	logger.Info().Str("conn", connID).Msg("closing connection due to expired TTL in cache")
	if m.expiryTimedOutCounter != nil {
		m.expiryTimedOutCounter.Inc()
	}
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
	m.updateMetrics(len(m.connIDToConn))
}

func (m *ConnMap) ClearUpdateQueues(userID, roomID string, nid int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, conn := range m.userIDToConn[userID] {
		conn.handler.PublishEventsUpTo(roomID, nid)
	}
}
