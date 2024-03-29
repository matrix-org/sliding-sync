package syncv3_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"golang.org/x/exp/slices"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/complement/client"
	"github.com/matrix-org/complement/must"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// The purpose of this test is to check that if Alice is in the room and is using the proxy, and then
// she leaves the room so no _proxy user_ is in the room, and events are sent whilst she is gone, that
// upon rejoining the proxy has A) the correct latest state and B) the correct latest timeline, such that
// the prev_batch token is correct to get earlier messages.
func TestRejoining(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	bob := registerNamedUser(t, "bob")
	charlie := registerNamedUser(t, "charlie")
	doris := registerNamedUser(t, "doris")

	// public chat => shared history visibility
	roomID := bob.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	alice.JoinRoom(t, roomID, nil)
	sendMessage(t, alice, roomID, "first message")
	firstTopicEventID := sendTopic(t, bob, roomID, "First Topic")

	// alice is the proxy user
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 5,
					RequiredState: [][2]string{{"*", "*"}}, // all state
				},
				Ranges:         sync3.SliceRanges{{0, 20}},
				BumpEventTypes: []string{"m.room.message"},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(
		map[string][]m.RoomMatcher{
			roomID: {
				m.MatchJoinCount(2),
				m.MatchRoomName(bob.Localpart),
				// The sliding sync request we just did was the first ever SS request for Alice.
				// This means the proxy will do an initial v2 request.
				// Despite asking for 5 timeline events, we only get 1 on the initial sync due to how the sync v2 filters are setup.
				// They are set up that way to ensure that prev_batch is always valid, when it is returned.
				MatchRoomTimeline([]Event{
					{
						ID: firstTopicEventID,
					},
				}),
			},
		},
	))

	// alice leaves the room => no proxy user in the room anymore!
	alice.MustLeaveRoom(t, roomID)
	res = alice.SlidingSyncUntilEvent(t, res.Pos, sync3.Request{}, roomID, Event{
		Content: map[string]interface{}{
			"membership": "leave",
		},
		Type:     "m.room.member",
		StateKey: &alice.UserID,
		Sender:   alice.UserID,
	})

	// now a few things happen:
	// - charlie joins the room (ensures joins are reflected)
	// - bob leaves the room (ensures leaves are reflected)
	// - doris is invited to the room (ensures invites are reflected)
	// - the topic is changed (ensures state updates)
	// - the room avatar is set (ensure avatar field updates and new state is added)
	// - 50 messages are sent (forcing a gappy sync)
	charlie.MustJoinRoom(t, roomID, []string{"hs1"})
	bob.MustInviteRoom(t, roomID, doris.UserID)
	newTopic := "New Topic Name"
	newAvatar := "mxc://example.org/JWEIFJgwEIhweiWJE"
	sendTopic(t, bob, roomID, newTopic)
	sendAvatar(t, bob, roomID, newAvatar)
	bob.MustLeaveRoom(t, roomID)
	for i := 0; i < 50; i++ {
		sendMessage(t, charlie, roomID, fmt.Sprintf("message %d", i))
	}

	// alice rejoins the room.
	alice.MustJoinRoom(t, roomID, []string{"hs1"})

	// we should get a 400 expired connection
	start := time.Now()
	for {
		httpRes := alice.DoSlidingSync(t, sync3.Request{}, WithPos(res.Pos))
		if httpRes.StatusCode == 400 {
			t.Logf("connection expired")
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("did not get expired connection on rejoin")
		}
		body := client.ParseJSON(t, httpRes)
		if err := json.Unmarshal(body, &res); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	aliceJoin := Event{
		Content: map[string]interface{}{
			"displayname": alice.Localpart,
			"membership":  "join",
		},
		Type:     "m.room.member",
		StateKey: &alice.UserID,
		Sender:   alice.UserID,
	}
	// ensure state is correct: new connection
	res = alice.SlidingSyncUntilEvent(t, "", sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 5,
					RequiredState: [][2]string{{"*", "*"}}, // all state
				},
				Ranges:         sync3.SliceRanges{{0, 20}},
				BumpEventTypes: []string{"m.room.message"},
			},
		},
	}, roomID, aliceJoin)
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchInviteCount(1), // doris
			m.MatchJoinCount(2),   // alice and charlie
			// Note: we only get 1 timeline event here because the proxy treats all rooms
			// as having history visibility = joined, so the join event is the earliest
			// thing she can see.
			MatchRoomTimeline([]Event{aliceJoin}),
			m.MatchRoomAvatar(newAvatar),
			MatchRoomRequiredState([]Event{
				{
					Type:     "m.room.topic",
					StateKey: b.Ptr(""),
					Content: map[string]interface{}{
						"topic": newTopic,
					},
				},
				{
					Type:     "m.room.avatar",
					StateKey: b.Ptr(""),
					Content: map[string]interface{}{
						"url": newAvatar,
					},
				},
				aliceJoin,
				{
					Content: map[string]interface{}{
						"displayname": charlie.Localpart,
						"membership":  "join",
					},
					Type:     "m.room.member",
					StateKey: &charlie.UserID,
					Sender:   charlie.UserID,
				},
				{
					Content: map[string]interface{}{
						"membership":  "invite",
						"displayname": doris.Localpart,
					},
					Type:     "m.room.member",
					StateKey: &doris.UserID,
					Sender:   bob.UserID,
				},
				{
					Content: map[string]interface{}{
						"membership": "leave",
					},
					Type:     "m.room.member",
					StateKey: &bob.UserID,
					Sender:   bob.UserID,
				},
			}),
		},
	}))

	// This response will not return a prev_batch token for the rejoin because the poller has already started,
	// so will be using a timeline limit of 50. This means the prev_batch from sync v2 will be for the N-50th
	// event, and SS wants to return a prev_batch for N-1th event (the join), but cannot. Subsequent events
	// will have a prev_batch though, so trigger one now. This also conveniently makes sure that alice is joined
	// to the room still.
	eventID := sendMessage(t, alice, roomID, "give me a prev batch please")
	res = alice.SlidingSyncUntilEvent(t, res.Pos, sync3.Request{}, roomID, Event{ID: eventID})
	m.MatchResponse(t, res, m.LogResponse(t))

	// pull out the prev_batch and check we can backpaginate correctly
	prevBatch := res.Rooms[roomID].PrevBatch
	must.NotEqual(t, prevBatch, "", "missing prev_batch")
	numScrollbackItems := 10
	scrollback := alice.Scrollback(t, roomID, prevBatch, numScrollbackItems)
	chunk := scrollback.Get("chunk").Array()
	var sbEvents []json.RawMessage
	for _, e := range chunk {
		sbEvents = append(sbEvents, json.RawMessage(e.Raw))
	}
	must.Equal(t, len(chunk), 10, "chunk length mismatch")
	var wantTimeline []Event
	for i := 0; i < (numScrollbackItems - 1); i++ {
		wantTimeline = append(wantTimeline, Event{
			Type: "m.room.message",
			Content: map[string]interface{}{
				"msgtype": "m.text",
				"body":    fmt.Sprintf("message %d", 41+i), // 40-49
			},
		})
	}
	wantTimeline = append(wantTimeline, aliceJoin)
	slices.Reverse(wantTimeline) // /messages returns in reverse chronological order
	must.NotError(t, "chunk mismatch", eventsEqual(wantTimeline, sbEvents))

	// charlie starts using the proxy, and he never had before. We should see him as joined
	// even though that happened in a "gappy sync". This is super important because charlie's
	// initial sync will include his join BUT we have already processed it so it'll be ignored,
	// meaning we rely entirely on alice's gappy sync to know charlie is joined.
	res = charlie.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 5,
					RequiredState: [][2]string{{"*", "*"}}, // all state
				},
				Ranges:         sync3.SliceRanges{{0, 20}},
				BumpEventTypes: []string{"m.room.message"},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			// charlie is allowed to see everything up to his join, so he sees alice's join
			// AND some earlier events
			MatchRoomTimeline([]Event{
				{
					Type: "m.room.message",
					Content: map[string]interface{}{
						"msgtype": "m.text",
						"body":    "message 47",
					},
				},
				{
					Type: "m.room.message",
					Content: map[string]interface{}{
						"msgtype": "m.text",
						"body":    "message 48",
					},
				},
				{
					Type: "m.room.message",
					Content: map[string]interface{}{
						"msgtype": "m.text",
						"body":    "message 49",
					},
				},
				aliceJoin,
				{ID: eventID}, // give me a prev batch message
			}),
			m.MatchInviteCount(1), // doris
			m.MatchJoinCount(2),   // alice and charlie
			m.MatchRoomAvatar(newAvatar),
		},
	}))

	// doris starts using the proxy, and she never had before. We should see her as invited
	// even though that happened in a "gappy sync". This is less interesting as the initial
	// sync will include the invite_state, but still worth checking.
	res = doris.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 5,
					RequiredState: [][2]string{{"*", "*"}}, // all state
				},
				Ranges:         sync3.SliceRanges{{0, 20}},
				BumpEventTypes: []string{"m.room.message"},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchRoomHasInviteState(),
		},
	}))

	// bob starts using the proxy, and he never had before. We should NOT see this room
	// as he left.
	res = bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 5,
					RequiredState: [][2]string{{"*", "*"}}, // all state
				},
				Ranges:         sync3.SliceRanges{{0, 20}},
				BumpEventTypes: []string{"m.room.message"},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{}))
}

func sendMessage(t *testing.T, client *CSAPI, roomID, text string) (eventID string) {
	return client.Unsafe_SendEventUnsynced(t, roomID, b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    text,
		},
	})
}

func sendTopic(t *testing.T, client *CSAPI, roomID, text string) (eventID string) {
	return client.Unsafe_SendEventUnsynced(t, roomID, b.Event{
		Type:     "m.room.topic",
		StateKey: b.Ptr(""),
		Content: map[string]interface{}{
			"topic": text,
		},
	})
}

func sendAvatar(t *testing.T, client *CSAPI, roomID, mxcURI string) (eventID string) {
	return client.Unsafe_SendEventUnsynced(t, roomID, b.Event{
		Type:     "m.room.avatar",
		StateKey: b.Ptr(""),
		Content: map[string]interface{}{
			"url": mxcURI,
		},
	})
}
