package syncv3_test

import (
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// Test that receipts are sent initially and during live streams
func TestReceipts(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)
	eventID := alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello world",
		},
	})
	alice.SendReceipt(t, roomID, eventID, "m.read")

	// bob syncs -> should see receipt
	res := bob.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
		Extensions: extensions.Request{
			Receipts: &extensions.ReceiptsRequest{
				Enabled: true,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			MatchRoomTimeline([]Event{
				{
					ID: eventID,
				},
			}),
		},
	}), m.MatchReceipts(roomID, []m.Receipt{
		{
			EventID: eventID,
			UserID:  alice.UserID,
			Type:    "m.read",
		},
	}))

	// bob sends receipt -> should see it on live stream
	bob.SendReceipt(t, roomID, eventID, "m.read")
	bob.SlidingSyncUntil(t, res.Pos, sync3.Request{}, func(r *sync3.Response) error {
		return m.MatchReceipts(roomID, []m.Receipt{
			{
				EventID: eventID,
				UserID:  bob.UserID,
				Type:    "m.read",
			},
		})(r)
	})
}

func TestReceiptsLazy(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	charlie := registerNewUser(t)
	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)
	charlie.JoinRoom(t, roomID, nil)
	alice.SlidingSync(t, sync3.Request{}) // proxy begins tracking
	eventID := alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello world",
		},
	})
	// everyone sends a receipt for this event
	alice.SendReceipt(t, roomID, eventID, "m.read")
	bob.SendReceipt(t, roomID, eventID, "m.read")
	charlie.SendReceipt(t, roomID, eventID, "m.read")

	// alice sends 5 new events, bob and alice ACK the last event
	var fifthEventID string
	for i := 0; i < 5; i++ {
		fifthEventID = alice.SendEventSynced(t, roomID, Event{
			Type: "m.room.message",
			Content: map[string]interface{}{
				"msgtype": "m.text",
				"body":    "Hello world",
			},
		})
	}
	alice.SendReceipt(t, roomID, fifthEventID, "m.read")
	bob.SendReceipt(t, roomID, fifthEventID, "m.read")

	// alice sends another 5 events and ACKs nothing
	var lastEventID string
	for i := 0; i < 5; i++ {
		lastEventID = alice.SendEventSynced(t, roomID, Event{
			Type: "m.room.message",
			Content: map[string]interface{}{
				"msgtype": "m.text",
				"body":    "Hello world",
			},
		})
	}
	// ensure the proxy has the last event processed
	alice.SlidingSyncUntilEventID(t, "", roomID, lastEventID)

	// Test that:
	// - Bob syncs with timeline_limit: 1 => only own receipt, as you always get your own receipts.
	// - Bob sync with timeline limit: 6 => receipts for fifthEventID only (self + alice)
	res := bob.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
		Extensions: extensions.Request{
			Receipts: &extensions.ReceiptsRequest{
				Enabled: true,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchReceipts(roomID, []m.Receipt{
		{
			EventID: fifthEventID,
			UserID:  bob.UserID,
			Type:    "m.read",
		},
	}))

	res = bob.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 6,
			},
		},
		Extensions: extensions.Request{
			Receipts: &extensions.ReceiptsRequest{
				Enabled: true,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchReceipts(roomID, []m.Receipt{
		{
			EventID: fifthEventID,
			UserID:  alice.UserID,
			Type:    "m.read",
		},
		{
			EventID: fifthEventID,
			UserID:  bob.UserID,
			Type:    "m.read",
		},
	}))
}

func TestReceiptsPrivate(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)

	eventID := alice.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello world",
		},
	})
	// bob secretly reads this
	bob.SendReceipt(t, roomID, eventID, "m.read.private")
	time.Sleep(300 * time.Millisecond) // TODO: find a better way to wait until the proxy has processed this.
	// alice does sliding sync -> does not see private RR
	res := alice.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
		Extensions: extensions.Request{
			Receipts: &extensions.ReceiptsRequest{
				Enabled: true,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchReceipts(roomID, nil))
	// bob does sliding sync -> sees private RR
	res = bob.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
		Extensions: extensions.Request{
			Receipts: &extensions.ReceiptsRequest{
				Enabled: true,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchReceipts(roomID, []m.Receipt{
		{
			UserID:  bob.UserID,
			EventID: eventID,
			Type:    "m.read.private",
		},
	}))
}
