package handler

import (
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"time"
)

type TxnIDWaiter struct {
	userID  string
	publish func(delayed bool, update caches.Update)
	// TODO: probably need a mutex around t.queues so the expiry won't race with enqueuing
	queues   map[string][]*caches.RoomEventUpdate
	maxDelay time.Duration
}

func NewTxnIDWaiter(userID string, maxDelay time.Duration, publish func(bool, caches.Update)) *TxnIDWaiter {
	return &TxnIDWaiter{
		userID:   userID,
		publish:  publish,
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
	// We only want to queue this event if
	//  - our user sent it AND it lacks a txn_id; OR
	//  - the room already has queued events.
	_, roomQueued := t.queues[ed.RoomID]
	missingTxnID := ed.Sender == t.userID && ed.TransactionID == ""
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
	logger.Trace().Str("room_id", ed.RoomID).Ints64("q", nids(t.queues[ed.RoomID])).Msgf("enqueue event NID %d", ed.NID)

	// TODO: if t gets gced, will this function still run? If so, will things explode?
	time.AfterFunc(t.maxDelay, func() { t.PublishUpToNID(ed.RoomID, ed.NID) })
}

func (t *TxnIDWaiter) PublishUpToNID(roomID string, publishNID int64) {
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

	logger.Trace().Str("room_id", roomID).Ints64("q", nids(queue)).Msgf("publish event up to NID %d", publishNID)
	for _, eventUpdate := range toPublish {
		t.publish(true, eventUpdate)
	}
}

func nids(updates []*caches.RoomEventUpdate) []int64 {
	rv := make([]int64, len(updates))
	for i, up := range updates {
		rv[i] = up.EventData.NID
	}
	return rv
}
