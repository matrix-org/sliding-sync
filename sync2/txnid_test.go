package sync2

import "testing"

func TestPendingTransactionIDs(t *testing.T) {
	pollingDevicesByUser := map[string][]string{
		"alice": {"A1", "A2"},
		"bob":   {"B1"},
		"chris": {},
		"delia": {"D1", "D2", "D3", "D4"},
		"enid":  {"E1", "E2"},
	}
	mockLoad := func(userID string) (deviceIDs []string) {
		devices, ok := pollingDevicesByUser[userID]
		if !ok {
			t.Fatalf("Mock didn't have devices for %s", userID)
		}
		newDevices := make([]string, len(devices))
		copy(newDevices, devices)
		return newDevices
	}

	pending := NewPendingTransactionIDs(mockLoad)

	// Alice.
	// We're tracking two of Alice's devices.
	allClear, err := pending.MissingTxnID("event1", "alice", "A1")
	assertNoError(t, err)
	assertAllClear(t, allClear, false) // waiting on A2

	// If for some reason the poller sees the same event for the same device, we should
	// still be waiting for A2.
	allClear, err = pending.MissingTxnID("event1", "alice", "A1")
	assertNoError(t, err)
	assertAllClear(t, allClear, false)

	// If for some reason Alice spun up a new device, we are still going to be waiting
	// for A2.
	allClear, err = pending.MissingTxnID("event1", "alice", "A_unknown_device")
	assertNoError(t, err)
	assertAllClear(t, allClear, false)

	// If A2 sees the event without a txnID, we should emit the all clear signal.
	allClear, err = pending.MissingTxnID("event1", "alice", "A2")
	assertNoError(t, err)
	assertAllClear(t, allClear, true)

	// If for some reason A2 sees the event a second time, we shouldn't re-emit the
	// all clear signal.
	allClear, err = pending.MissingTxnID("event1", "alice", "A2")
	assertNoError(t, err)
	assertAllClear(t, allClear, false)

	// Bob.
	// We're only tracking one device for Bob
	allClear, err = pending.MissingTxnID("event2", "bob", "B1")
	assertNoError(t, err)
	assertAllClear(t, allClear, true) // not waiting on any devices

	// Chris.
	// We're not tracking any devices for Chris. A MissingTxnID call for him shouldn't
	// cause anything to explode.
	allClear, err = pending.MissingTxnID("event3", "chris", "C_unknown_device")
	assertNoError(t, err)

	// Delia.
	// Delia is tracking four devices.
	allClear, err = pending.MissingTxnID("event4", "delia", "D1")
	assertNoError(t, err)
	assertAllClear(t, allClear, false) // waiting on E2, E3 and E4

	// One of Delia's devices, say D2, sees a txn ID for E4.
	err = pending.SeenTxnID("event4")
	assertNoError(t, err)

	// The other devices see the event. Neither should emit all clear.
	allClear, err = pending.MissingTxnID("event4", "delia", "D3")
	assertNoError(t, err)
	assertAllClear(t, allClear, false)

	allClear, err = pending.MissingTxnID("event4", "delia", "D4")
	assertNoError(t, err)
	assertAllClear(t, allClear, false)

	// Enid.
	// Enid has two devices. Her first poller (E1) is lucky and sees the transaction ID.
	err = pending.SeenTxnID("event5")
	assertNoError(t, err)

	// Her second poller misses the transaction ID, but this shouldn't cause an all clear.
	allClear, err = pending.MissingTxnID("event4", "delia", "E2")
	assertNoError(t, err)
	assertAllClear(t, allClear, false)
}

func assertAllClear(t *testing.T, got bool, want bool) {
	if got != want {
		t.Errorf("Expected allClear=%t, got %t", want, got)
	}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("got error: %s", err)
	}
}
