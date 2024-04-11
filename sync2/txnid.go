package sync2

import (
	"fmt"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v2"
)

type loaderFunc func(userID string) (deviceIDs []string)

// PendingTransactionIDs is (conceptually) a map from event IDs to a list of device IDs.
// Its keys are the IDs of event we've seen which a) lack a transaction ID, and b) were
// sent by one of the users we are polling for. The values are the list of the sender's
// devices whose pollers are yet to see a transaction ID.
//
// If another poller sees the same event
//
//   - with a transaction ID, it emits a V2TransactionID payload with that ID and
//     removes the event ID from this map.
//
//   - without a transaction ID, it removes the polling device ID from the values
//     list. If the device ID list is now empty, the poller emits an "all clear"
//     V2TransactionID payload.
//
// This is a best-effort affair to ensure that the rest of the proxy can wait for
// transaction IDs to appear before transmitting an event down /sync to its sender.
//
// It's possible that we add an entry to this map and then the list of remaining
// device IDs becomes out of date, either due to a new device creation or an
// existing device expiring. We choose not to handle this case, because it is relatively
// rare.
//
// To avoid the map growing without bound, we use a ttlcache and drop entries
// after a short period of time.
type PendingTransactionIDs struct {
	// mu guards the pending field. See MissingTxnID for rationale.
	mu      sync.Mutex
	pending *ttlcache.Cache
	// loader should provide the list of device IDs
	loader loaderFunc
}

func NewPendingTransactionIDs(loader loaderFunc) *PendingTransactionIDs {
	c := ttlcache.NewCache()
	c.SetTTL(5 * time.Minute)     // keep transaction IDs for 5 minutes before forgetting about them
	c.SkipTTLExtensionOnHit(true) // we don't care how many times they ask for the item, 5min is the limit.
	return &PendingTransactionIDs{
		mu:      sync.Mutex{},
		pending: c,
		loader:  loader,
	}
}

// MissingTxnID should be called to report that this device ID did not see a
// transaction ID for this event ID. Returns true if this is the first time we know
// for sure that we'll never see a txn ID for this event.
func (c *PendingTransactionIDs) MissingTxnID(eventID, userID, myDeviceID string) (bool, error) {
	// While ttlcache is threadsafe, it does not provide a way to atomically update
	// (get+set) a value, which means we are still open to races. For example:
	//
	//  - We have three pollers A, B, C.
	//  - Poller A sees an event without txn id and calls MissingTxnID.
	//  - `c.pending.Get()` fails, so we load up all device IDs: [A, B, C].
	//  - Then `c.pending.Set()` with [B, C].
	//  - Poller B sees the same event, also missing txn ID and calls MissingTxnID.
	//  - Poller C does the same concurrently.
	//
	// If the Get+Set isn't atomic, then we might do e.g.
	//  - B gets [B, C] and prepares to write [C].
	//  - C gets [B, C] and prepares to write [B].
	//  - Last writer wins. Either way, we never write [] and so never return true
	//    (the all-clear signal.)
	//
	// This wouldn't be the end of the world (the API process has a maximum delay, and
	// the ttlcache will expire the entry), but it would still be nice to avoid it.
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := c.pending.Get(eventID)
	if err == ttlcache.ErrNotFound {
		data = c.loader(userID)
	} else if err != nil {
		return false, fmt.Errorf("PendingTransactionIDs: failed to get device ids: %w", err)
	}

	deviceIDs, ok := data.([]string)
	if !ok {
		return false, fmt.Errorf("PendingTransactionIDs: failed to cast device IDs")
	}

	deviceIDs, changed := removeDevice(myDeviceID, deviceIDs)
	if changed {
		err = c.pending.Set(eventID, deviceIDs)
		if err != nil {
			return false, fmt.Errorf("PendingTransactionIDs: failed to set device IDs: %w", err)
		}
	}
	return changed && len(deviceIDs) == 0, nil
}

// SeenTxnID should be called to report that this device saw a transaction ID
// for this event.
func (c *PendingTransactionIDs) SeenTxnID(eventID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending.Set(eventID, []string{})
}

// removeDevice takes a device ID slice and returns a device ID slice with one
// particular string removed. Assumes that the given slice has no duplicates.
// Does not modify the given slice in situ.
func removeDevice(device string, devices []string) ([]string, bool) {
	for i, otherDevice := range devices {
		if otherDevice == device {
			return append(devices[:i], devices[i+1:]...), true
		}
	}
	return devices, false
}
