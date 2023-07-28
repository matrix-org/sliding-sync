package handler

import (
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"sync"
	"time"
)

type TxnIDWaiter struct {
	userID  string
	publish func(delayed bool, update caches.Update)
	// mu guards the queues map.
	mu       sync.Mutex
	queues   map[string][]*caches.RoomEventUpdate
	maxDelay time.Duration
}

func NewTxnIDWaiter(userID string, maxDelay time.Duration, publish func(bool, caches.Update)) *TxnIDWaiter {
	return &TxnIDWaiter{
		userID:   userID,
		publish:  publish,
		mu:       sync.Mutex{},
		queues:   make(map[string][]*caches.RoomEventUpdate),
		maxDelay: maxDelay,
		// TODO: metric that tracks how long events were queued for.
	}
}

func (t *TxnIDWaiter) Ingest(up caches.Update) {
	if t.maxDelay <= 0 {
		t.publish(false, up)
		return
	}

	eventUpdate, isEventUpdate := up.(*caches.RoomEventUpdate)
	if !isEventUpdate {
		t.publish(false, up)
		return
	}

	ed := eventUpdate.EventData

	// An event should be queued if
	//  - it's a state event that our user sent, lacking a txn_id; OR
	//  - the room already has queued events.
	t.mu.Lock()
	defer t.mu.Unlock()
	_, roomQueued := t.queues[ed.RoomID]
	missingTxnID := ed.StateKey == nil && ed.Sender == t.userID && ed.TransactionID == ""
	if !(missingTxnID || roomQueued) {
		t.publish(false, up)
		return
	}

	// We've decided to queue the event.
	queue, exists := t.queues[ed.RoomID]
	if !exists {
		queue = make([]*caches.RoomEventUpdate, 0, 10)
	}
	// TODO: bound the queue size?
	t.queues[ed.RoomID] = append(queue, eventUpdate)

	time.AfterFunc(t.maxDelay, func() { t.PublishUpToNID(ed.RoomID, ed.NID) })
}

func (t *TxnIDWaiter) PublishUpToNID(roomID string, publishNID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	queue, exists := t.queues[roomID]
	if !exists {
		return
	}

	var i int
	for i = 0; i < len(queue); i++ {
		// Scan forwards through the queue until we find an event with nid > publishNID.
		if queue[i].EventData.NID > publishNID {
			break
		}
	}
	// Now queue[:i] has events with nid <= publishNID, and queue[i:] has nids > publishNID.
	// strip off the first i events from the slice and publish them.
	toPublish, queue := queue[:i], queue[i:]
	if len(queue) == 0 {
		delete(t.queues, roomID)
	} else {
		t.queues[roomID] = queue
	}

	for _, eventUpdate := range toPublish {
		t.publish(true, eventUpdate)
	}
}
