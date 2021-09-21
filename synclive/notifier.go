package synclive

import (
	"context"
	"sync"
	"time"

	"github.com/ReneKroon/ttlcache/v2"
)

type Notifier struct {
	cache *ttlcache.Cache

	// map of user_id to active connections. Inspect the ConnID to find the device ID.
	userIDToConn map[string][]*Conn
	// map of room_id to joined user IDs.
	joinedUsers map[string][]string
	mu          *sync.Mutex
}

func NewNotifier() *Notifier {
	cm := &Notifier{
		userIDToConn: make(map[string][]*Conn),
		joinedUsers:  make(map[string][]string),
		mu:           &sync.Mutex{},
	}
	cm.cache = ttlcache.NewCache()
	cm.cache.SetTTL(30 * time.Minute) // TODO: customisable
	return cm
}

func (m *Notifier) Conn(cid ConnID) *Conn {
	cint, _ := m.cache.Get(cid.String())
	if cint == nil {
		return nil
	}
	return cint.(*Conn)
}

func (m *Notifier) SetConn(conn *Conn) {
	m.cache.Set(conn.ConnID.String(), conn)
}

func (m *Notifier) connsForRoom(roomID string) []*Conn {
	return nil
}

// Implements Conn.HandleIncomingRequest
func (m *Notifier) HandleIncomingRequest(ctx context.Context, connID ConnID, reqBody []byte) ([]byte, error) {
	return reqBody, nil // echobot
}

func (m *Notifier) OnNewEvent(
	roomID, sender, eventType, stateKey, membership string, userIDs []string,
) {
	notifyConns := m.connsForRoom(roomID)
	for _, uid := range userIDs {
		conns := m.userIDToConn[uid]
		if len(conns) > 0 {
			notifyConns = append(notifyConns, conns...)
		}
	}
	// de-duplicate the conns

	// check if conn isn't blocking this event in their filter / ranges

	// send message
}
