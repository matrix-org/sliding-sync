package syncv3_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/gjson"
)

func TestNumLive(t *testing.T) {
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
			"body":    "Hello World!!!!",
		},
	})

	// initial syncs -> no live events
	res := bob.SlidingSync(t, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(
		map[string][]m.RoomMatcher{
			roomID: {
				MatchRoomTimelineContains(Event{ID: eventID}),
				m.MatchNumLive(0),
			},
		},
	))

	// live event -> 1 num live
	eventID2 := alice.SendEventSynced(t, roomID, b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello World!!!! 2",
		},
	})
	res = bob.SlidingSyncUntilEventID(t, res.Pos, roomID, eventID2)
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(
		map[string][]m.RoomMatcher{
			roomID: {
				MatchRoomTimelineContains(Event{ID: eventID2}),
				m.MatchNumLive(1),
			},
		},
	))

	// from fresh => 0 num live
	res = bob.SlidingSyncUntilEventID(t, "", roomID, eventID2)
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(
		map[string][]m.RoomMatcher{
			roomID: {
				MatchRoomTimelineContains(Event{ID: eventID2}),
				m.MatchNumLive(0),
			},
		},
	))

	// now the big one -> 3 rooms, ask for top 2, bump 3rd room to top twice -> num_live=2
	roomID2 := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID2, nil)
	roomID3 := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID3, nil)
	// let the proxy become aware of these joins
	alice.SlidingSyncUntilMembership(t, "", roomID3, bob, "join")
	// at this point, roomID is at the bottom, check.
	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 1}}, // top 2 rooms
				Sort:   []string{sync3.SortByRecency},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 2,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Ops(
		m.MatchV3SyncOp(0, 1, []string{roomID3, roomID2}),
	)))
	// now send 2 live events into roomID to bump it to the top
	eventID3 := alice.SendEventSynced(t, roomID, b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "bump 1",
		},
	})
	eventID4 := alice.SendEventSynced(t, roomID, b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "bump 2",
		},
	})
	// wait for the proxy to get it
	alice.SlidingSyncUntilEventID(t, "", roomID, eventID4)
	// now syncing with bob should see 2 live events
	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 1}}, // top 2 rooms
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchNumLive(2),
			MatchRoomTimeline([]Event{{ID: eventID2}, {ID: eventID3}, {ID: eventID4}}),
		},
	}))
}

// Test that if you constantly change req params, we still see live traffic. It does this by:
// - Creating 11 rooms.
// - Hitting /sync with a range [0,1] then [0,2] then [0,3]. Each time this causes a new room to be returned.
// - Interleaving each /sync request with genuine events sent into a room.
// - ensuring we see the genuine events by the time we finish.
func TestReqParamStarvation(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomID := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	numOtherRooms := 10
	for i := 0; i < numOtherRooms; i++ {
		bob.MustCreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
		})
	}
	bob.JoinRoom(t, roomID, nil)
	res := bob.SlidingSyncUntilMembership(t, "", roomID, bob, "join")

	wantEventIDs := make(map[string]bool)
	for i := 0; i < numOtherRooms; i++ {
		res = bob.SlidingSync(t, sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Ranges: sync3.SliceRanges{{0, int64(i)}}, // [0,0], [0,1], ... [0,9]
				},
			},
		}, WithPos(res.Pos))

		// mark off any event we see in wantEventIDs
		for _, r := range res.Rooms {
			for _, ev := range r.Timeline {
				gotEventID := gjson.GetBytes(ev, "event_id").Str
				wantEventIDs[gotEventID] = false
			}
		}

		// send an event in the first few syncs to add to wantEventIDs
		// We do this for the first few /syncs and don't dictate which response they should arrive
		// in, as we do not know and cannot force the proxy to deliver the event in a particular response.
		if i < 3 {
			eventID := alice.SendEventSynced(t, roomID, b.Event{
				Type: "m.room.message",
				Content: map[string]interface{}{
					"msgtype": "m.text",
					"body":    fmt.Sprintf("msg %d", i),
				},
			})
			wantEventIDs[eventID] = true
		}

		// it's possible the proxy won't see this event before the next /sync
		// and that is the reason why we don't send it, as opposed to starvation.
		// To try to counter this, sleep a bit. This is why we sleep on every cycle and
		// why we send the events early on.
		time.Sleep(50 * time.Millisecond)
	}

	// at this point wantEventIDs should all have false values if we got the events
	for evID, unseen := range wantEventIDs {
		if unseen {
			t.Errorf("failed to see event %v", evID)
		}
	}
}
