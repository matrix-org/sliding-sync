package syncv3_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// Test that receipts are sent initially and during live streams
func TestReceipts(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomID := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)
	eventID := alice.SendEventSynced(t, roomID, b.Event{
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
				Core: extensions.Core{Enabled: &boolTrue},
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
	roomID := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)
	charlie.JoinRoom(t, roomID, nil)
	alice.SlidingSync(t, sync3.Request{}) // proxy begins tracking
	eventID := alice.SendEventSynced(t, roomID, b.Event{
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
		fifthEventID = alice.SendEventSynced(t, roomID, b.Event{
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
		lastEventID = alice.SendEventSynced(t, roomID, b.Event{
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
				Core: extensions.Core{Enabled: &boolTrue},
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
				Core: extensions.Core{Enabled: &boolTrue},
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
	roomID := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)

	eventID := alice.SendEventSynced(t, roomID, b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello world",
		},
	})
	// bob secretly reads this
	bob.SendReceipt(t, roomID, eventID, "m.read.private")
	time.Sleep(300 * time.Millisecond) // TODO: find a better way to wait until the proxy has processed this.
	// bob does sliding sync -> sees private RR
	res := bob.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
		Extensions: extensions.Request{
			Receipts: &extensions.ReceiptsRequest{
				Core: extensions.Core{Enabled: &boolTrue},
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
	// alice does sliding sync -> does not see private RR
	// We do this _after_ bob's sync request so we know we got the private RR and it is actively
	// suppressed, rather than the private RR not making it to the proxy yet.
	res = alice.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
		Extensions: extensions.Request{
			Receipts: &extensions.ReceiptsRequest{
				Core: extensions.Core{Enabled: &boolTrue},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchNoReceiptsExtension())
}

func TestReceiptsRespectsExtensionScope(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)

	var syncResp *sync3.Response

	t.Log("Alice creates four rooms.")
	room1 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 1"})
	room2 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 2"})
	room3 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 3"})
	room4 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 4"})
	t.Logf("room1=%s room2=%s room3=%s room4=%s", room1, room2, room3, room4)

	t.Log("Bob joins those rooms.")
	bob.JoinRoom(t, room1, nil)
	bob.JoinRoom(t, room2, nil)
	bob.JoinRoom(t, room3, nil)
	bob.JoinRoom(t, room4, nil)

	t.Log("Alice posts a message to each room")
	messageEvent := b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello, room!",
		},
	}
	message1 := alice.SendEventSynced(t, room1, messageEvent)
	message2 := alice.SendEventSynced(t, room2, messageEvent)
	message3 := alice.SendEventSynced(t, room3, messageEvent)
	message4 := alice.SendEventSynced(t, room4, messageEvent)

	t.Log("Bob posts a public read receipt for the messages in rooms 1 and 2.")
	bob.SendReceipt(t, room1, message1, "m.read")
	bob.SendReceipt(t, room2, message2, "m.read")

	t.Log("Bob makes an initial sliding sync, requesting receipts in rooms 2 and 4 only.")
	syncResp = bob.SlidingSync(t, sync3.Request{
		Extensions: extensions.Request{
			Receipts: &extensions.ReceiptsRequest{
				Core: extensions.Core{Enabled: &boolTrue, Lists: []string{}, Rooms: []string{room2, room4}},
			},
		},
		Lists: map[string]sync3.RequestList{
			"window": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	})

	t.Log("Bob should see his receipt in room 2, but not his receipt in room 1.")
	m.MatchResponse(
		t,
		syncResp,
		m.MatchReceipts(room1, nil),
		m.MatchReceipts(room2, []m.Receipt{{
			EventID: message2,
			UserID:  bob.UserID,
			Type:    "m.read",
		}}),
	)

	t.Log("Bob posts receipts for rooms 3 and 4.")
	bob.SendReceipt(t, room3, message3, "m.read")
	bob.SendReceipt(t, room4, message4, "m.read")

	t.Log("Bob sees his receipt in room 4, but not in room 3.")
	syncResp = bob.SlidingSyncUntil(
		t,
		syncResp.Pos,
		sync3.Request{}, // no change
		func(response *sync3.Response) error {
			// Need to check some receipts data exist, or room3 matcher will fail
			if response.Extensions.Receipts == nil {
				return fmt.Errorf("no receipts data in sync response")
			}

			// Bob never sees a receipt in room 3.
			matcherRoom3 := m.MatchReceipts(room3, nil)
			err := matcherRoom3(response)
			if err != nil {
				t.Error(err)
			}

			matcherRoom4 := m.MatchReceipts(room4, []m.Receipt{{
				EventID: message4,
				UserID:  bob.UserID,
				Type:    "m.read",
			}})
			return matcherRoom4(response)
		},
	)
}

func TestReceiptsOnRoomsOnly(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	bob := registerNamedUser(t, "bob")

	t.Log("Alice creates two rooms.")
	room1 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 1"})
	room2 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 2"})
	t.Logf("room1=%s room2=%s", room1, room2)

	t.Log("Bob joins those rooms.")
	bob.JoinRoom(t, room1, nil)
	bob.JoinRoom(t, room2, nil)

	t.Log("Alice posts a message to each room")
	messageEvent := b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello, room!",
		},
	}
	message1 := alice.SendEventSynced(t, room1, messageEvent)
	message2 := alice.SendEventSynced(t, room2, messageEvent)

	t.Log("Bob posts a public read receipt for the messages in both rooms.")
	bob.SendReceipt(t, room1, message1, "m.read")
	bob.SendReceipt(t, room2, message2, "m.read")

	t.Log("Bob makes an initial sliding sync, with a window to capture room 1 and a subscription to room 2.")
	t.Log("He requests receipts for his explicit room subscriptions only.")
	res := bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"room1": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 20,
				},
				Ranges: sync3.SliceRanges{{0, 0}},
				Sort:   []string{"by_name"},
			},
		},
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			room2: {
				TimelineLimit: 20,
			},
		},
		Extensions: extensions.Request{
			Receipts: &extensions.ReceiptsRequest{
				Core: extensions.Core{Enabled: &boolTrue, Lists: []string{}, Rooms: nil},
			},
		},
	})

	t.Log("Bob should see the messages in both rooms, but only a receipt in room 2.")
	m.MatchResponse(
		t,
		res,
		m.MatchList("room1", m.MatchV3Count(2), m.MatchV3Ops(m.MatchV3SyncOp(0, 0, []string{room1}))),
		m.MatchRoomSubscription(room1, MatchRoomTimelineMostRecent(1, []Event{{ID: message1}})),
		m.MatchRoomSubscription(room2, MatchRoomTimelineMostRecent(1, []Event{{ID: message2}})),
		m.MatchReceipts(room1, nil),
		m.MatchReceipts(room2, []m.Receipt{{
			EventID: message2,
			UserID:  bob.UserID,
			Type:    "m.read",
		}}),
	)

	// Now do the same, but live-streaming.

	t.Log("Alice posts another message to each room")
	message3 := alice.SendEventSynced(t, room1, messageEvent)
	message4 := alice.SendEventSynced(t, room2, messageEvent)

	t.Log("Bob posts a public read receipt for the messages both rooms.")
	bob.SendReceipt(t, room1, message3, "m.read")
	bob.SendReceipt(t, room2, message4, "m.read")

	seenMsg3 := false
	seenMsg4 := false
	seenReceipt4 := false
	res = bob.SlidingSyncUntil(t, res.Pos, sync3.Request{}, func(response *sync3.Response) error {
		matchMsg3 := m.MatchRoomSubscription(room1, MatchRoomTimelineMostRecent(1, []Event{{ID: message3}}))
		matchMsg4 := m.MatchRoomSubscription(room2, MatchRoomTimelineMostRecent(1, []Event{{ID: message4}}))

		if matchMsg3(response) == nil {
			seenMsg3 = true
		}
		if matchMsg4(response) == nil {
			seenMsg4 = true
		}

		matchNoReceiptsRoom1 := m.MatchReceipts(room1, nil)
		matchReceiptRoom2 := m.MatchReceipts(room2, []m.Receipt{{
			EventID: message4,
			UserID:  bob.UserID,
			Type:    "m.read",
		}})

		if err := matchNoReceiptsRoom1(response); err != nil {
			return err
		}

		if matchReceiptRoom2(response) == nil {
			seenReceipt4 = true
		}

		if !(seenMsg3 && seenMsg4 && seenReceipt4) {
			return fmt.Errorf("still waiting: seenMsg3 = %t, seenMsg4 = %t, seenReceipt4 = %t", seenMsg3, seenMsg4, seenReceipt4)
		}
		return nil
	})
}
