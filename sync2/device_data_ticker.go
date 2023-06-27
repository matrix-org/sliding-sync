package sync2

import (
	"sync"
	"time"

	"github.com/matrix-org/sliding-sync/pubsub"
)

// This struct remembers user+device IDs to notify for then periodically
// emits them all to the caller. Use to rate limit the frequency of device list
// updates.
type DeviceDataTicker struct {
	// data structures to periodically notify downstream about device data updates
	// The ticker controls the frequency of updates. The done channel is used to stop ticking
	// and clean up the goroutine. The notify map contains the values to notify for.
	ticker    *time.Ticker
	done      chan struct{}
	notifyMap *sync.Map // map of PollerID to bools, unwrapped when notifying
	fn        func(payload *pubsub.V2DeviceData)
}

// Create a new device data ticker, which batches calls to Remember and invokes a callback every
// d duration. If d is 0, no batching is performed and the callback is invoked synchronously, which
// is useful for testing.
func NewDeviceDataTicker(d time.Duration) *DeviceDataTicker {
	ddt := &DeviceDataTicker{
		done:      make(chan struct{}),
		notifyMap: &sync.Map{},
	}
	if d != 0 {
		ddt.ticker = time.NewTicker(d)
	}
	return ddt
}

// Stop ticking.
func (t *DeviceDataTicker) Stop() {
	if t.ticker != nil {
		t.ticker.Stop()
	}
	close(t.done)
}

// Set the function which should be called when the tick happens.
func (t *DeviceDataTicker) SetCallback(fn func(payload *pubsub.V2DeviceData)) {
	t.fn = fn
}

// Remember this user/device ID, and emit it later on.
func (t *DeviceDataTicker) Remember(pid PollerID) {
	t.notifyMap.Store(pid, true)
	if t.ticker == nil {
		t.emitUpdate()
	}
}

func (t *DeviceDataTicker) emitUpdate() {
	var p pubsub.V2DeviceData
	p.UserIDToDeviceIDs = make(map[string][]string)
	// populate the pubsub payload
	t.notifyMap.Range(func(key, value any) bool {
		pid := key.(PollerID)
		devices := p.UserIDToDeviceIDs[pid.UserID]
		devices = append(devices, pid.DeviceID)
		p.UserIDToDeviceIDs[pid.UserID] = devices
		// clear the map of this value
		t.notifyMap.Delete(key)
		return true // keep enumerating
	})
	// notify if we have entries
	if len(p.UserIDToDeviceIDs) > 0 {
		t.fn(&p)
	}
}

// Blocks forever, ticking until Stop() is called.
func (t *DeviceDataTicker) Run() {
	if t.ticker == nil {
		return
	}
	for {
		select {
		case <-t.done:
			return
		case <-t.ticker.C:
			t.emitUpdate()
		}
	}
}
