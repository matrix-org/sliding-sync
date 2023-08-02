package syncv3_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

func TestTransactionIDsAppear(t *testing.T) {
	client := registerNewUser(t)
	roomID := client.CreateRoom(t, map[string]interface{}{})
	sendRes := client.MustDoFunc(t, "PUT", []string{"_matrix", "client", "v3", "rooms", roomID, "send", "m.room.message", "foobar"},
		WithJSONBody(t, map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello World!",
		}))
	body := ParseJSON(t, sendRes)
	eventID := GetJSONFieldStr(t, body, "event_id")

	// ensure initial syncs include the txn id
	res := client.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges:           [][2]int64{{0, 10}},
				RoomSubscription: sync3.RoomSubscription{TimelineLimit: 1},
			},
		},
	})

	// we cannot use MatchTimeline here because the Unsigned section contains 'age' which is not
	// deterministic and MatchTimeline does not do partial matches.

	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			matchTransactionID(t, eventID, "foobar"),
		},
	}))

	// now live stream another event and ensure that too has a txn ID
	sendRes = client.MustDoFunc(t, "PUT", []string{"_matrix", "client", "v3", "rooms", roomID, "send", "m.room.message", "foobar2"},
		WithJSONBody(t, map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello World 2!",
		}))
	body = ParseJSON(t, sendRes)
	eventID = GetJSONFieldStr(t, body, "event_id")

	res = client.SlidingSyncUntilEvent(t, res.Pos, sync3.Request{}, roomID, Event{ID: eventID})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			matchTransactionID(t, eventID, "foobar2"),
		},
	}))

}

// This test has 1 poller expecting a txn ID and 10 others that won't see one.
// We test that sending device sees a txnID. Without the TxnIDWaiter logic in place,
// this test is likely (but not guaranteed) to fail.
func TestTransactionIDsAppearWithMultiplePollers(t *testing.T) {
	alice := registerNamedUser(t, "alice")

	t.Log("Alice creates a room and syncs until she sees it.")
	roomID := alice.CreateRoom(t, map[string]interface{}{})
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 10,
				},
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscription(roomID))

	t.Log("Alice makes other devices and starts them syncing.")
	for i := 0; i < 10; i++ {
		device := *alice
		device.Login(t, "password", fmt.Sprintf("device_%d", i))
		device.SlidingSync(t, sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					RoomSubscription: sync3.RoomSubscription{
						TimelineLimit: 10,
					},
					Ranges: sync3.SliceRanges{{0, 20}},
				},
			},
		})
	}

	t.Log("Alice sends a message with a transaction ID.")
	const txnID = "foobar"
	sendRes := alice.MustDoFunc(t, "PUT", []string{"_matrix", "client", "v3", "rooms", roomID, "send", "m.room.message", txnID},
		WithJSONBody(t, map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello, world!",
		}))
	body := ParseJSON(t, sendRes)
	eventID := GetJSONFieldStr(t, body, "event_id")

	t.Log("Alice syncs on her main devices until she sees her message.")
	res = alice.SlidingSyncUntilEventID(t, res.Pos, roomID, eventID)

	m.MatchResponse(t, res, m.MatchRoomSubscription(roomID, matchTransactionID(t, eventID, txnID)))
}

func matchTransactionID(t *testing.T, eventID, txnID string) m.RoomMatcher {
	return func(r sync3.Room) error {
		for _, ev := range r.Timeline {
			var got Event
			if err := json.Unmarshal(ev, &got); err != nil {
				return fmt.Errorf("failed to unmarshal event: %s", err)
			}
			if got.ID != eventID {
				continue
			}
			tx, ok := got.Unsigned["transaction_id"]
			if !ok {
				return fmt.Errorf("unsigned block for %s has no transaction_id", eventID)
			}
			gotTxnID := tx.(string)
			if gotTxnID != txnID {
				return fmt.Errorf("wrong transaction_id, got %s want %s", gotTxnID, txnID)
			}
			t.Logf("%s has txn ID %s", eventID, gotTxnID)
			return nil
		}
		return fmt.Errorf("not found event %s", eventID)
	}
}
