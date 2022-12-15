package handler

import (
	"sync"

	"github.com/matrix-org/sliding-sync/pubsub"
)

type pendingInfo struct {
	done bool
	ch   chan struct{}
}

type EnsurePoller struct {
	chanName     string
	mu           *sync.Mutex
	pendingPolls map[string]pendingInfo
	notifier     pubsub.Notifier
}

func NewEnsurePoller(notifier pubsub.Notifier) *EnsurePoller {
	return &EnsurePoller{
		chanName:     pubsub.ChanV3,
		mu:           &sync.Mutex{},
		pendingPolls: make(map[string]pendingInfo),
		notifier:     notifier,
	}
}

// EnsurePolling blocks until the V2InitialSyncComplete response is received for this device. It is
// the caller's responsibility to call OnInitialSyncComplete when new events arrive.
func (p *EnsurePoller) EnsurePolling(userID, deviceID string) {
	key := userID + "|" + deviceID
	p.mu.Lock()
	// do we need to wait?
	if p.pendingPolls[key].done {
		p.mu.Unlock()
		return
	}
	// have we called EnsurePolling for this user/device before?
	ch := p.pendingPolls[key].ch
	if ch != nil {
		p.mu.Unlock()
		// we already called EnsurePolling on this device, so just listen for the close
		// TODO: several times there have been problems getting the response back from the poller
		// we should time out here after 100s and return an error or something to kick conns into
		// trying again
		<-ch
		return
	}
	// Make a channel to wait until we have done an initial sync
	ch = make(chan struct{})
	p.pendingPolls[key] = pendingInfo{
		done: false,
		ch:   ch,
	}
	p.mu.Unlock()
	// ask the pollers to poll for this device
	p.notifier.Notify(p.chanName, &pubsub.V3EnsurePolling{
		UserID:   userID,
		DeviceID: deviceID,
	})
	// if by some miracle the notify AND sync completes before we receive on ch then this is
	// still fine as recv on a closed channel will return immediately.
	<-ch
}

func (p *EnsurePoller) OnInitialSyncComplete(payload *pubsub.V2InitialSyncComplete) {
	key := payload.UserID + "|" + payload.DeviceID
	p.mu.Lock()
	defer p.mu.Unlock()
	pending, ok := p.pendingPolls[key]
	// were we waiting for this initial sync to complete?
	if !ok {
		// This can happen when the v2 poller spontaneously starts polling even without us asking it to
		// e.g from the database
		p.pendingPolls[key] = pendingInfo{
			done: true,
		}
		return
	}
	if pending.done {
		// nothing to do, we just got OnInitialSyncComplete called twice
		return
	}
	// we get here if we asked the poller to start via EnsurePolling, so let's make that goroutine
	// wake up now
	ch := pending.ch
	pending.done = true
	pending.ch = nil
	p.pendingPolls[key] = pending
	close(ch)
}

func (p *EnsurePoller) Teardown() {
	p.notifier.Close()
}
