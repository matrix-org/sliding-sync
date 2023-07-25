package handler

import (
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

type TxnIDWaiter struct {
	userID  string
	publish func(update caches.Update)
	queued  map[string][]caches.Update
}

func NewTxnIDWaiter(userID string, publish func(caches.Update)) *TxnIDWaiter {
	return &TxnIDWaiter{
		userID:  userID,
		publish: publish,
		queued:  make(map[string][]caches.Update),
	}
}

func (t *TxnIDWaiter) Ingest(up caches.Update) {
	// TODO: investigate whether this update needs to be queued.
	// TODO: bound the queue size?
	t.publish(up)
}
