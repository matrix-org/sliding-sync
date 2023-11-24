package sync3

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/complement/must"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

const (
	alice = "@alice:localhost"
	bob   = "@bob:localhost"
)

func TestConnMap(t *testing.T) {
	cm := NewConnMap(false, time.Minute)
	cid := ConnID{UserID: alice, DeviceID: "A", CID: "room-list"}
	_, cancel := context.WithCancel(context.Background())
	conn := cm.CreateConn(cid, cancel, func() ConnHandler {
		return &mockConnHandler{}
	})
	must.Equal(t, conn.ConnID, cid, "cid mismatch")

	// lookups work
	must.Equal(t, cm.Conn(cid), conn, "*Conn wasn't the same when fetched via Conn(ConnID)")
	conns := cm.Conns(cid.UserID, cid.DeviceID)
	must.Equal(t, len(conns), 1, "Conns length mismatch")
	must.Equal(t, conns[0], conn, "*Conn wasn't the same when fetched via Conns()[0]")
}

func TestConnMap_CloseConnsForDevice(t *testing.T) {
	cm := NewConnMap(false, time.Minute)
	otherCID := ConnID{UserID: bob, DeviceID: "A", CID: "room-list"}
	cidToConn := map[ConnID]*Conn{
		{UserID: alice, DeviceID: "A", CID: "room-list"}:     nil,
		{UserID: alice, DeviceID: "A", CID: "encryption"}:    nil,
		{UserID: alice, DeviceID: "A", CID: "notifications"}: nil,
		{UserID: alice, DeviceID: "B", CID: "room-list"}:     nil,
		{UserID: alice, DeviceID: "B", CID: "encryption"}:    nil,
		{UserID: alice, DeviceID: "B", CID: "notifications"}: nil,
		otherCID: nil,
	}
	for cid := range cidToConn {
		_, cancel := context.WithCancel(context.Background())
		conn := cm.CreateConn(cid, cancel, func() ConnHandler {
			return &mockConnHandler{}
		})
		cidToConn[cid] = conn
	}

	closedDevice := "A"
	cm.CloseConnsForDevice(alice, closedDevice)
	time.Sleep(100 * time.Millisecond) // some stuff happens asyncly in goroutines

	// Destroy should have been called for all alice|A connections
	assertDestroyedConns(t, cidToConn, func(cid ConnID) bool {
		return cid.UserID == alice && cid.DeviceID == "A"
	})
}

func TestConnMap_CloseConnsForUser(t *testing.T) {
	cm := NewConnMap(false, time.Minute)
	otherCID := ConnID{UserID: bob, DeviceID: "A", CID: "room-list"}
	cidToConn := map[ConnID]*Conn{
		{UserID: alice, DeviceID: "A", CID: "room-list"}:     nil,
		{UserID: alice, DeviceID: "A", CID: "encryption"}:    nil,
		{UserID: alice, DeviceID: "A", CID: "notifications"}: nil,
		{UserID: alice, DeviceID: "B", CID: "room-list"}:     nil,
		{UserID: alice, DeviceID: "B", CID: "encryption"}:    nil,
		{UserID: alice, DeviceID: "B", CID: "notifications"}: nil,
		otherCID: nil,
	}
	for cid := range cidToConn {
		_, cancel := context.WithCancel(context.Background())
		conn := cm.CreateConn(cid, cancel, func() ConnHandler {
			return &mockConnHandler{}
		})
		cidToConn[cid] = conn
	}

	num := cm.CloseConnsForUsers([]string{alice})
	time.Sleep(100 * time.Millisecond) // some stuff happens asyncly in goroutines
	must.Equal(t, num, 6, "unexpected number of closed conns")

	// Destroy should have been called for all alice|A connections
	assertDestroyedConns(t, cidToConn, func(cid ConnID) bool {
		return cid.UserID == alice
	})
}

func TestConnMap_TTLExpiry(t *testing.T) {
	cm := NewConnMap(false, time.Second) // 1s expiry
	expiredCIDs := []ConnID{
		{UserID: alice, DeviceID: "A", CID: "room-list"},
		{UserID: alice, DeviceID: "A", CID: "encryption"},
		{UserID: alice, DeviceID: "A", CID: "notifications"},
	}
	cidToConn := map[ConnID]*Conn{}
	for _, cid := range expiredCIDs {
		_, cancel := context.WithCancel(context.Background())
		conn := cm.CreateConn(cid, cancel, func() ConnHandler {
			return &mockConnHandler{}
		})
		cidToConn[cid] = conn
	}
	time.Sleep(time.Millisecond * 500)

	unexpiredCIDs := []ConnID{
		{UserID: alice, DeviceID: "B", CID: "room-list"},
		{UserID: alice, DeviceID: "B", CID: "encryption"},
		{UserID: alice, DeviceID: "B", CID: "notifications"},
	}
	for _, cid := range unexpiredCIDs {
		_, cancel := context.WithCancel(context.Background())
		conn := cm.CreateConn(cid, cancel, func() ConnHandler {
			return &mockConnHandler{}
		})
		cidToConn[cid] = conn
	}

	time.Sleep(510 * time.Millisecond) // all 'A' device conns must have expired

	// Destroy should have been called for all alice|A connections
	assertDestroyedConns(t, cidToConn, func(cid ConnID) bool {
		return cid.DeviceID == "A"
	})
}

func assertDestroyedConns(t *testing.T, cidToConn map[ConnID]*Conn, isDestroyedFn func(cid ConnID) bool) {
	t.Helper()
	for cid, conn := range cidToConn {
		if isDestroyedFn(cid) {
			must.Equal(t, conn.handler.(*mockConnHandler).isDestroyed, true, fmt.Sprintf("conn %+v was not destroyed", cid))
		} else {
			must.Equal(t, conn.handler.(*mockConnHandler).isDestroyed, false, fmt.Sprintf("conn %+v was destroyed", cid))
		}
	}
}

type mockConnHandler struct {
	isDestroyed bool
	cancel      context.CancelFunc
}

func (c *mockConnHandler) OnIncomingRequest(ctx context.Context, cid ConnID, req *Request, isInitial bool, start time.Time) (*Response, error) {
	return nil, nil
}
func (c *mockConnHandler) OnUpdate(ctx context.Context, update caches.Update) {}
func (c *mockConnHandler) PublishEventsUpTo(roomID string, nid int64)         {}
func (c *mockConnHandler) Destroy() {
	c.isDestroyed = true
}
func (c *mockConnHandler) Alive() bool {
	return true // buffer never fills up
}
func (c *mockConnHandler) SetCancelCallback(cancel context.CancelFunc) {
	c.cancel = cancel
}
