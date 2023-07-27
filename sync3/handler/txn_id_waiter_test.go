package handler

import (
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"testing"
	"time"
)

type publishArg struct {
	delayed bool
	update  caches.Update
}

// Test that
// - events are (reported as being) delayed when we expect them to be
// - delayed events are automatically published after the maximum delay period
func TestTxnIDWaiter_QueuingLogic(t *testing.T) {
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

// Test that PublishUpToNID
//   - correctly pops off the start of the queue
//   - is idempotent
//   - deletes map entry if queue is empty (so that roomQueued is set correctly)
func TestTxnIDWaiter_PublishUpToNID(t *testing.T) {
	const alice = "@alice:example.com"
	const room = "!unimportant"
	var published []publishArg
	publish := func(delayed bool, update caches.Update) {
		published = append(published, publishArg{delayed, update})
	}
	// Use an hour's expiry to effectively disable expiry.
	w := NewTxnIDWaiter(alice, time.Hour, publish)
	// Ingest 5 events, each of which would be queued by themselves.
	for i := int64(2); i <= 6; i++ {
		w.Ingest(&caches.RoomEventUpdate{
			EventData: &caches.EventData{
				RoomID:        room,
				Sender:        alice,
				TransactionID: "",
				NID:           i,
			},
		})
	}

	t.Log("Queue has nids [2,3,4,5,6]")
	t.Log("Publishing up to 1 should do nothing")
	w.PublishUpToNID(room, 1)
	assertNIDs(t, published, nil)

	t.Log("Publishing up to 3 should yield nids [2, 3] in that order")
	w.PublishUpToNID(room, 3)
	assertNIDs(t, published, []int64{2, 3})
	assertDelayed(t, published[:2])

	t.Log("Publishing up to 3 a second time should do nothing")
	w.PublishUpToNID(room, 3)
	assertNIDs(t, published, []int64{2, 3})

	t.Log("Publishing up to 2 at this point should do nothing.")
	w.PublishUpToNID(room, 2)
	assertNIDs(t, published, []int64{2, 3})

	t.Log("Publishing up to 6 should yield nids [4, 5, 6] in that order")
	w.PublishUpToNID(room, 6)
	assertNIDs(t, published, []int64{2, 3, 4, 5, 6})
	assertDelayed(t, published[2:5])

	t.Log("Publishing up to 6 a second time should do nothing")
	w.PublishUpToNID(room, 6)
	assertNIDs(t, published, []int64{2, 3, 4, 5, 6})

	t.Log("Ingesting another event that doesn't need to be queueing should be published immediately")
	w.Ingest(&caches.RoomEventUpdate{
		EventData: &caches.EventData{
			RoomID:        room,
			Sender:        "@notalice:example.com",
			TransactionID: "",
			NID:           7,
		},
	})
	assertNIDs(t, published, []int64{2, 3, 4, 5, 6, 7})
	if published[len(published)-1].delayed {
		t.Errorf("Final event was delayed, but should have been published immediately")
	}
}

// Test that PublishUpToNID only publishes in the given room
func TestTxnIDWaiter_PublishUpToNID_MultipleRooms(t *testing.T) {
	const alice = "@alice:example.com"
	var published []publishArg
	publish := func(delayed bool, update caches.Update) {
		published = append(published, publishArg{delayed, update})
	}
	// Use an hour's expiry to effectively disable expiry.
	w := NewTxnIDWaiter(alice, time.Hour, publish)
	// Ingest four queueable events across two rooms.
	w.Ingest(&caches.RoomEventUpdate{
		EventData: &caches.EventData{
			RoomID:        "!room1",
			Sender:        alice,
			TransactionID: "",
			NID:           1,
		},
	})
	w.Ingest(&caches.RoomEventUpdate{
		EventData: &caches.EventData{
			RoomID:        "!room2",
			Sender:        alice,
			TransactionID: "",
			NID:           2,
		},
	})
	w.Ingest(&caches.RoomEventUpdate{
		EventData: &caches.EventData{
			RoomID:        "!room2",
			Sender:        alice,
			TransactionID: "",
			NID:           3,
		},
	})
	w.Ingest(&caches.RoomEventUpdate{
		EventData: &caches.EventData{
			RoomID:        "!room1",
			Sender:        alice,
			TransactionID: "",
			NID:           4,
		},
	})

	t.Log("Queues are [1, 4] and [2, 3]")
	t.Log("Publish up to NID 4 in room 1 should yield nids [1, 4]")
	w.PublishUpToNID("!room1", 4)
	assertNIDs(t, published, []int64{1, 4})
	assertDelayed(t, published)

	t.Log("Queues are [1, 4] and [2, 3]")
	t.Log("Publish up to NID 3 in room 2 should yield nids [2, 3]")
	w.PublishUpToNID("!room2", 3)
	assertNIDs(t, published, []int64{1, 4, 2, 3})
	assertDelayed(t, published)
}

func assertDelayed(t *testing.T, published []publishArg) {
	for _, p := range published {
		if !p.delayed {
			t.Errorf("published arg with NID %d was not delayed, but we expected it to be", p.update.(*caches.RoomEventUpdate).EventData.NID)
		}
	}
}

func assertNIDs(t *testing.T, published []publishArg, expectedNIDs []int64) {
	if len(published) != len(expectedNIDs) {
		t.Errorf("Got %d nids, but expected %d", len(published), len(expectedNIDs))
	}
	for i := range published {
		rup, ok := published[i].update.(*caches.RoomEventUpdate)
		if !ok {
			t.Errorf("Update %d (%v) was not a RoomEventUpdate", i, published[i].update)
		}
		if rup.EventData.NID != expectedNIDs[i] {
			t.Errorf("Update %d (%v) got nid %d, expected %d", i, *rup, rup.EventData.NID, expectedNIDs[i])
		}
	}
}
