package handler

import (
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"testing"
	"time"
)

func TestTxnIDWaiterQueuingLogic(t *testing.T) {
	const alice = "alice"
	const bob = "bob"
	const room1 = "!theroom"
	const room2 = "!daszimmer"

	testCases := []struct {
		Name          string
		Ingest        []caches.Update
		WaitForUpdate int
		ExpectDelayed bool
	}{
		{
			Name:          "empty queue, non-event update",
			Ingest:        []caches.Update{&caches.AccountDataUpdate{}},
			WaitForUpdate: 0,
			ExpectDelayed: false,
		},
		{
			Name: "empty queue, event update, another sender",
			Ingest: []caches.Update{
				&caches.RoomEventUpdate{
					EventData: &caches.EventData{
						RoomID: room1,
						Sender: bob,
					},
				}},
			WaitForUpdate: 0,
			ExpectDelayed: false,
		},
		{
			Name: "empty queue, event update, has txn_id",
			Ingest: []caches.Update{
				&caches.RoomEventUpdate{
					EventData: &caches.EventData{
						RoomID:        room1,
						Sender:        alice,
						TransactionID: "txntxntxn",
					},
				}},
			WaitForUpdate: 0,
			ExpectDelayed: false,
		},
		{
			Name: "empty queue, event update, no txn_id",
			Ingest: []caches.Update{
				&caches.RoomEventUpdate{
					EventData: &caches.EventData{
						RoomID:        room1,
						Sender:        alice,
						TransactionID: "",
					},
				}},
			WaitForUpdate: 0,
			ExpectDelayed: true,
		},
		{
			Name: "nonempty queue, non-event update",
			Ingest: []caches.Update{
				&caches.RoomEventUpdate{
					EventData: &caches.EventData{
						RoomID:        room1,
						Sender:        alice,
						TransactionID: "",
						NID:           1,
					},
				},
				&caches.AccountDataUpdate{},
			},
			WaitForUpdate: 1,
			ExpectDelayed: false, // not a room event, no need to queued behind alice's event
		},
		{
			Name: "nonempty queue, event update, different sender",
			Ingest: []caches.Update{
				&caches.RoomEventUpdate{
					EventData: &caches.EventData{
						RoomID:        room1,
						Sender:        alice,
						TransactionID: "",
						NID:           1,
					},
				},
				&caches.RoomEventUpdate{
					EventData: &caches.EventData{
						RoomID: room1,
						Sender: bob,
						NID:    2,
					},
				},
			},
			WaitForUpdate: 1,
			ExpectDelayed: true, // should be queued behind alice's event
		},
		{
			Name: "nonempty queue, event update, has txn_id",
			Ingest: []caches.Update{
				&caches.RoomEventUpdate{
					EventData: &caches.EventData{
						RoomID:        room1,
						Sender:        alice,
						TransactionID: "",
						NID:           1,
					},
				},
				&caches.RoomEventUpdate{
					EventData: &caches.EventData{
						RoomID:        room1,
						Sender:        alice,
						NID:           2,
						TransactionID: "I have a txn",
					},
				},
			},
			WaitForUpdate: 1,
			ExpectDelayed: true, // should still be queued behind alice's first event
		},
		{
			Name: "existence of queue only matters per-room",
			Ingest: []caches.Update{
				&caches.RoomEventUpdate{
					EventData: &caches.EventData{
						RoomID:        room1,
						Sender:        alice,
						TransactionID: "",
						NID:           1,
					},
				},
				&caches.RoomEventUpdate{
					EventData: &caches.EventData{
						RoomID:        room2,
						Sender:        alice,
						NID:           2,
						TransactionID: "I have a txn",
					},
				},
			},
			WaitForUpdate: 1,
			ExpectDelayed: false, // queue only tracks room1
		},
	}

	type publishArg struct {
		delayed bool
		update  caches.Update
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			updates := make(chan publishArg, 100)
			publish := func(delayed bool, update caches.Update) {
				updates <- publishArg{delayed, update}
			}

			w := NewTxnIDWaiter(alice, time.Millisecond, publish)

			for _, up := range tc.Ingest {
				w.Ingest(up)
			}

			wantedUpdate := tc.Ingest[tc.WaitForUpdate]
			var got publishArg
		WaitForSelectedUpdate:
			for {
				select {
				case got = <-updates:
					t.Logf("Got update %v", got.update)
					if got.update == wantedUpdate {
						break WaitForSelectedUpdate
					}
				case <-time.After(5 * time.Millisecond):
					t.Fatalf("Did not see update %v published", wantedUpdate)
				}
			}

			if got.delayed != tc.ExpectDelayed {
				t.Errorf("Got delayed=%t want delayed=%t", got.delayed, tc.ExpectDelayed)
			}
		})
	}
}

// TODO: tests which demonstrate that PublishEventsUpTo()
//   - correctly pops off the start of the queue
//   - is idempotent
//   - only affects the given room ID
//   - deletes map entry if queue is empty (so that roomQueued is set correctly)
