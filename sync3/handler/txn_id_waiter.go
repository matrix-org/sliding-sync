package handler

import (
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

type TxnIDWaiter struct {
	userID              string
	publish             func(update caches.Update)
	subscribedOrVisible func(roomID string) bool
	queued              map[string][]caches.Update
}

func NewTxnIDWaiter(userID string, publish func(caches.Update), subscribedOrVisible func(string) bool) *TxnIDWaiter {
	return &TxnIDWaiter{
		userID:              userID,
		publish:             publish,
		subscribedOrVisible: subscribedOrVisible,
		queued:              make(map[string][]caches.Update),
	}
}

func (t *TxnIDWaiter) Ingest(up caches.Update) {
	if !t.shouldQueue(up) {
		t.publish(up)
	}

	// TODO: bound the queue size?
	// TODO: enqueue and timeout
}

func (t *TxnIDWaiter) shouldQueue(up caches.Update) bool {
	e, isEventUpdate := up.(*caches.RoomEventUpdate)
	if isEventUpdate {
		// TODO: ensure we don't keep length-0 or nil slices in the queued map so this works correctly.
		_, roomQueued := t.queued[e.EventData.RoomID]
		if (e.EventData.Sender == t.userID || roomQueued) && t.subscribedOrVisible(e.EventData.RoomID) {
			return true
		}
	}
	return false
}
