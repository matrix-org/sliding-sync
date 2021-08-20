package syncv3

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3/streams"
	"github.com/matrix-org/sync-v3/testutils"
	"github.com/tidwall/gjson"
)

// This tests all the moving parts for the sync v3 server. It does the following:
// - Initialise a single room between alice on bob on the v2 side. Tests that the v2 poller is glued in to the storage code.
// - Specify a typing filter and call v3 sync. This should block and timeout as there are no typing notifs for this room.
//   Tests that the notifier can block and time out.
// - Set bob to typing in the room and call v3 sync WITHOUT a typing filter. Tests that the server remembers filters
//   and that the typing filter works.
// - Call v3 sync again and after 100ms inject a new typing response into the v2 stream. Tests that v2 responses poke the
//   notifier which then pokes the v3 sync.
func TestHandler(t *testing.T) {
	alice := "@alice:localhost"
	aliceBearer := "Bearer alice_access_token"
	bob := "@bob:localhost"
	charlie := "@charlie:localhost"
	roomID := "!foo:localhost"

	server, v2Client := newSync3Server(t)
	aliceV2Stream := v2Client.v2StreamForUser(alice, aliceBearer)

	// prepare a response from v2
	v2Resp := &sync2.SyncResponse{
		NextBatch: "don't care",
	}
	v2Resp.Rooms.Join = make(map[string]sync2.SyncV2JoinResponse)
	v2Resp.Rooms.Join[roomID] = sync2.SyncV2JoinResponse{
		State: struct {
			Events []json.RawMessage `json:"events"`
		}{
			Events: []json.RawMessage{
				marshalJSON(t, map[string]interface{}{
					"event_id": "$1", "sender": bob, "type": "m.room.create", "state_key": "", "content": map[string]interface{}{
						"creator": bob,
					}}),
				marshalJSON(t, map[string]interface{}{
					"event_id": "$2", "sender": bob, "type": "m.room.join_rules", "state_key": "", "content": map[string]interface{}{
						"join_rule": "public",
					}}),
				marshalJSON(t, map[string]interface{}{
					"event_id": "$3", "sender": bob, "type": "m.room.member", "state_key": bob, "content": map[string]interface{}{
						"membership": "join",
					}}),
				marshalJSON(t, map[string]interface{}{
					"event_id": "$4", "sender": alice, "type": "m.room.member", "state_key": alice, "content": map[string]interface{}{
						"membership": "join",
					}}),
			},
		},
	}
	waitUntilV2Processed := aliceV2Stream(v2Resp)

	// fresh user should make a new session and start polling, getting these events above.
	// however, we didn't ask for them so they shouldn't be returned and should timeout with no data
	v3resp := mustDoSync3Request(t, server, aliceBearer, "", map[string]interface{}{
		"typing": map[string]interface{}{
			"room_id": roomID,
		},
	})
	if v3resp.Typing != nil {
		t.Fatalf("expected no data due to timeout, got data: %+v", v3resp.Typing)
	}

	waitUntilV2Processed()

	// now set bob to typing
	v2Resp = &sync2.SyncResponse{
		NextBatch: "still don't care",
	}
	v2Resp.Rooms.Join = make(map[string]sync2.SyncV2JoinResponse)
	v2Resp.Rooms.Join[roomID] = sync2.SyncV2JoinResponse{
		Ephemeral: struct {
			Events []json.RawMessage `json:"events"`
		}{
			Events: []json.RawMessage{
				marshalJSON(t, map[string]interface{}{
					"type": "m.typing", "room_id": roomID, "content": map[string]interface{}{
						"user_ids": []string{bob},
					},
				}),
			},
		},
	}
	waitUntilV2Processed = aliceV2Stream(v2Resp)

	// 2nd request with no special args should remember we want the typing notif
	v3resp = mustDoSync3Request(t, server, aliceBearer, v3resp.Next, map[string]interface{}{})

	waitUntilV2Processed()

	// Check that the response returns bob typing
	if v3resp.Typing == nil {
		t.Fatalf("no typing response, wanted one")
	}
	if len(v3resp.Typing.UserIDs) != 1 {
		t.Fatalf("typing got %d users, want 1: %v", len(v3resp.Typing.UserIDs), v3resp.Typing.UserIDs)
	}
	if v3resp.Typing.UserIDs[0] != bob {
		t.Fatalf("typing got %s want %s", v3resp.Typing.UserIDs[0], bob)
	}

	// inject a new v2 response after 200ms which should wake up the sync stream
	go func() {
		time.Sleep(200 * time.Millisecond)
		// now set charlie to typing
		v2Resp = &sync2.SyncResponse{
			NextBatch: "still still don't care",
		}
		v2Resp.Rooms.Join = make(map[string]sync2.SyncV2JoinResponse)
		v2Resp.Rooms.Join[roomID] = sync2.SyncV2JoinResponse{
			Ephemeral: struct {
				Events []json.RawMessage `json:"events"`
			}{
				Events: []json.RawMessage{
					marshalJSON(t, map[string]interface{}{
						"type": "m.typing", "room_id": roomID, "content": map[string]interface{}{
							"user_ids": []string{charlie},
						},
					}),
				},
			},
		}
		aliceV2Stream(v2Resp)
	}()
	v3resp = mustDoSync3Request(t, server, aliceBearer, v3resp.Next, map[string]interface{}{})

	// Check that the response returns charlie typing
	if v3resp.Typing == nil {
		t.Fatalf("no typing response, wanted one")
	}
	if len(v3resp.Typing.UserIDs) != 1 {
		t.Fatalf("typing got %d users, want 1: %v", len(v3resp.Typing.UserIDs), v3resp.Typing.UserIDs)
	}
	if v3resp.Typing.UserIDs[0] != charlie {
		t.Fatalf("typing got %s want %s", v3resp.Typing.UserIDs[0], charlie)
	}
}

// Test to_device stream:
// - Injecting a to_device event gets received.
// - `limit` is honoured.
// - Repeating the request without having ACKed the position returns the event again.
// - After ACKing the position, going back to the old position returns no event.
// - If 2 sessions exist, both session must ACK the position before the event is deleted.
func TestHandlerToDevice(t *testing.T) {
	server, v2Client := newSync3Server(t)
	t.Run("Injecting a to_device event into the v2 stream gets received", func(t *testing.T) {
		alice := "@alice:localhost"
		aliceBearer := "Bearer alice_access_token"
		aliceV2Stream := v2Client.v2StreamForUser(alice, aliceBearer)

		// prepare a response from v2
		toDeviceEvent := gomatrixserverlib.SendToDeviceEvent{
			Sender:  alice,
			Type:    "to_device.test",
			Content: []byte(`{"foo":"bar"}`),
		}
		v2Resp := &sync2.SyncResponse{
			NextBatch: "don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{
					toDeviceEvent,
				},
			},
		}
		aliceV2Stream(v2Resp)

		v3resp := mustDoSync3Request(t, server, aliceBearer, "", map[string]interface{}{
			"to_device": map[string]interface{}{},
		})
		if v3resp.ToDevice == nil {
			t.Fatalf("expected to_device response, got none: %+v", v3resp)
		}
		if len(v3resp.ToDevice.Events) != 1 {
			t.Fatalf("expected 1 to_device message, got %d", len(v3resp.ToDevice.Events))
		}
		want, _ := json.Marshal(toDeviceEvent)
		if !bytes.Equal(v3resp.ToDevice.Events[0], want) {
			t.Fatalf("wrong event returned, got %s want %s", string(v3resp.ToDevice.Events[0]), string(want))
		}
	})
	t.Run("'limit' is honoured and fetches earliest events first'", func(t *testing.T) {
		numMsgs := 300
		userLimit := 20

		bob := "@bob:localhost"
		bobBearer := "Bearer bob_access_token"
		bobV2Stream := v2Client.v2StreamForUser(bob, bobBearer)

		// prepare a response from v2
		v2Resp := &sync2.SyncResponse{
			NextBatch: "don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{},
			},
		}

		for i := 0; i < numMsgs; i++ {
			v2Resp.ToDevice.Events = append(v2Resp.ToDevice.Events, gomatrixserverlib.SendToDeviceEvent{
				Sender:  bob,
				Type:    "to_device.test_limit",
				Content: []byte(fmt.Sprintf(`{"foo":"bar %d"}`, i)),
			})
		}

		bobV2Stream(v2Resp)

		v3resp := mustDoSync3Request(t, server, bobBearer, "", map[string]interface{}{
			"to_device": map[string]interface{}{
				"limit": userLimit,
			},
		})
		if v3resp.ToDevice == nil {
			t.Fatalf("expected to_device response, got none: %+v", v3resp)
		}
		if len(v3resp.ToDevice.Events) > userLimit {
			t.Fatalf("expected %d to_device message, got %d", userLimit, len(v3resp.ToDevice.Events))
		}
		// should return earliest events first
		for i := 0; i < userLimit; i++ {
			want, _ := json.Marshal(v2Resp.ToDevice.Events[i])
			if !bytes.Equal(v3resp.ToDevice.Events[i], want) {
				t.Fatalf("wrong event returned, got %s want %s", string(v3resp.ToDevice.Events[i]), string(want))
			}
		}
		// next request gets the next batch of limited events
		v3resp = mustDoSync3Request(t, server, bobBearer, v3resp.Next, map[string]interface{}{
			"to_device": map[string]interface{}{
				"limit": userLimit,
			},
		})
		if v3resp.ToDevice == nil {
			t.Fatalf("expected to_device response, got none: %+v", v3resp)
		}
		if len(v3resp.ToDevice.Events) > userLimit {
			t.Fatalf("expected %d to_device message, got %d", userLimit, len(v3resp.ToDevice.Events))
		}
		// should return earliest events first
		for i := 0; i < userLimit; i++ {
			j := userLimit + i
			want, _ := json.Marshal(v2Resp.ToDevice.Events[j])
			if !bytes.Equal(v3resp.ToDevice.Events[i], want) {
				t.Fatalf("wrong event returned, got %s want %s", string(v3resp.ToDevice.Events[i]), string(want))
			}
		}
	})
	t.Run("Repeating the request without having ACKed the position returns the event again", func(t *testing.T) {
		charlie := "@charlie:localhost"
		charlieBearer := "Bearer charlie_access_token"
		charlieV2Stream := v2Client.v2StreamForUser(charlie, charlieBearer)

		// prepare a response from v2
		v2Resp := &sync2.SyncResponse{
			NextBatch: "don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{
					{
						Sender:  charlie,
						Type:    "to_device.dummy-event-to-get-a-since-token",
						Content: []byte(`{"foo":"bar"}`),
					},
				},
			},
		}
		charlieV2Stream(v2Resp)
		v3respAnchor := mustDoSync3Request(t, server, charlieBearer, "", map[string]interface{}{
			"to_device": map[string]interface{}{},
		})
		// now we have a ?since= token we can use, inject the actual event
		toDeviceEvent := gomatrixserverlib.SendToDeviceEvent{
			Sender:  charlie,
			Type:    "to_device.test.this.should.work",
			Content: []byte(`{"foo":"bar2"}`),
		}
		v2Resp = &sync2.SyncResponse{
			NextBatch: "still don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{
					toDeviceEvent,
				},
			},
		}
		charlieV2Stream(v2Resp)

		// do the same request 3 times with the first since token. Because we never increment the
		// since token it should keep returning the same event
		var v3resp *streams.Response
		for i := 0; i < 3; i++ {
			v3resp = mustDoSync3Request(t, server, charlieBearer, v3respAnchor.Next, map[string]interface{}{
				"to_device": map[string]interface{}{},
			})
			if v3resp.ToDevice == nil {
				t.Fatalf("expected to_device response, got none: %+v", v3resp)
			}
			if len(v3resp.ToDevice.Events) != 1 {
				t.Fatalf("expected 1 to_device message, got %d", len(v3resp.ToDevice.Events))
			}
			want, _ := json.Marshal(toDeviceEvent)
			if !bytes.Equal(v3resp.ToDevice.Events[0], want) {
				t.Fatalf("wrong event returned, got %s want %s", string(v3resp.ToDevice.Events[0]), string(want))
			}
		}
		t.Run("After ACKing the position, going back to the old position returns no event", func(t *testing.T) {
			t.Logf("acking pos %v", v3resp.Next)
			// ACK the position
			_ = mustDoSync3Request(t, server, charlieBearer, v3resp.Next, map[string]interface{}{
				"to_device": map[string]interface{}{},
			})
			t.Logf("going back to pos %v", v3respAnchor.Next)
			// now requests for the previous position return nothing as the to_device event was deleted
			v3resp = mustDoSync3Request(t, server, charlieBearer, v3respAnchor.Next, map[string]interface{}{
				"to_device": map[string]interface{}{},
			})
			if v3resp.ToDevice == nil {
				t.Fatalf("expected to_device response, got none: %+v", v3resp)
			}
			if len(v3resp.ToDevice.Events) != 0 {
				t.Fatalf("expected 0 to_device message, got %d", len(v3resp.ToDevice.Events))
			}
		})
	})
	t.Run("If 2 sessions exist, both session must ACK the position before the event is deleted", func(t *testing.T) {
		doris := "@doris:localhost"
		dorisBearer := "Bearer doris_access_token"
		dorisV2Stream := v2Client.v2StreamForUser(doris, dorisBearer)
		// prepare a response from v2
		v2Resp := &sync2.SyncResponse{
			NextBatch: "don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{
					{
						Sender:  doris,
						Type:    "to_device.dummy-event-to-get-a-since-token",
						Content: []byte(`{"foo":"bar"}`),
					},
				},
			},
		}
		dorisV2Stream(v2Resp)
		// do 2 calls to /sync without a since token == make 2 sessions
		s1v3respAnchor := mustDoSync3Request(t, server, dorisBearer, "", map[string]interface{}{
			"to_device": map[string]interface{}{},
		})
		s2v3respAnchor := mustDoSync3Request(t, server, dorisBearer, "", map[string]interface{}{
			"to_device": map[string]interface{}{},
		})
		// now we have a ?since= token we can use, inject the actual event
		toDeviceEvent := gomatrixserverlib.SendToDeviceEvent{
			Sender:  doris,
			Type:    "to_device.test.this.should.work",
			Content: []byte(`{"foo":"bar2"}`),
		}
		v2Resp = &sync2.SyncResponse{
			NextBatch: "still don't care",
			ToDevice: struct {
				Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
			}{
				Events: []gomatrixserverlib.SendToDeviceEvent{
					toDeviceEvent,
				},
			},
		}
		dorisV2Stream(v2Resp)
		// calling /sync with session 1 and session 2 now returns the event
		sinceValues := []string{s1v3respAnchor.Next, s2v3respAnchor.Next}
		for i, since := range sinceValues {
			v3resp := mustDoSync3Request(t, server, dorisBearer, since, map[string]interface{}{
				"to_device": map[string]interface{}{},
			})
			if v3resp.ToDevice == nil {
				t.Fatalf("expected to_device response, got none: %+v", v3resp)
			}
			if len(v3resp.ToDevice.Events) != 1 {
				t.Fatalf("expected 1 to_device message, got %d", len(v3resp.ToDevice.Events))
			}
			// update to the latest token
			sinceValues[i] = v3resp.Next
		}

		// up to this point neither session have ACKed the event even though they got it, because they
		// haven't made a request with the latest token, so do it with session 1.
		_ = mustDoSync3Request(t, server, dorisBearer, sinceValues[0], map[string]interface{}{
			"to_device": map[string]interface{}{},
		})

		// now, doing the old request on session 2 should still return the event as session 2 never
		// ACKed the event
		v3resp := mustDoSync3Request(t, server, dorisBearer, s2v3respAnchor.Next, map[string]interface{}{
			"to_device": map[string]interface{}{},
		})
		if v3resp.ToDevice == nil {
			t.Fatalf("expected to_device response, got none: %+v", v3resp)
		}
		if len(v3resp.ToDevice.Events) != 1 {
			t.Fatalf("expected 1 to_device message, got %d", len(v3resp.ToDevice.Events))
		}
	})
}

type memlog struct { // @alice:localhost => "join"
	Sender     string
	Target     string
	Membership string
}

const EmptySince = -1

type memrequest struct {
	Limit                      int
	Sort                       string
	WantUserIDs                []string
	SinceRequestIndex          int // the request index to use the next batch from as a since value
	UsePrevP                   bool
	InjectMembersBeforeRequest []memlog
}

// Test room member stream:
// - Can get all room members in a DM room and an empty since token
// - Gets the first page of room members in a big group room and an empty since token
// - Gets the 2nd page (and so on) of room members in a big group room until all members are received
// - If the state changes from underneath the client whilst paginating, the pagination state remains the same
// - Getting chronological deltas with a since token works (like typical sync v2)
// - sorting by PL/name works
// - limit on streaming works
func TestHandlerRoomMember(t *testing.T) {
	server, v2Client := newSync3Server(t)
	alice := "@alice:localhost"
	bob := "@bob:localhost"
	charlie := "@charlie:localhost"
	doris := "@doris:localhost"
	eve := "@eve:localhost"
	frank := "@frank:localhost"
	aliceBearer := "Bearer alice_access_token"
	aliceV2Stream := v2Client.v2StreamForUser(alice, aliceBearer)

	testCases := []struct {
		Name              string // test name
		Creator           string // @alice:localhost, implicitly joined from start
		PL                *gomatrixserverlib.PowerLevelContent
		StateMemberLog    []memlog
		TimelineMemberLog []memlog
		Names             map[string]string // @alice:localhost => "Alice"
		Requests          []memrequest
	}{
		{
			Name:    "Can get all room members in a DM room and an empty since token and a known since token",
			Creator: alice,
			TimelineMemberLog: []memlog{
				{
					Sender:     bob,
					Target:     bob,
					Membership: "join",
				},
			},
			Requests: []memrequest{
				{
					// default limit should be >2 and sort order should be by PL then name
					WantUserIDs:       []string{alice, bob},
					SinceRequestIndex: EmptySince,
				},
			},
		},
		{
			Name:    "Can paginate in a big group room and an empty since token",
			Creator: alice,
			StateMemberLog: []memlog{
				{
					Sender:     eve,
					Target:     eve,
					Membership: "join",
				},
				{
					Sender:     charlie,
					Target:     charlie,
					Membership: "join",
				},
			},
			TimelineMemberLog: []memlog{
				{
					Sender:     doris,
					Target:     doris,
					Membership: "join",
				},
				{
					Sender:     bob,
					Target:     bob,
					Membership: "join",
				},
			},
			Requests: []memrequest{
				{
					Limit:             2,
					Sort:              "by_name",
					WantUserIDs:       []string{alice, bob},
					SinceRequestIndex: EmptySince,
				},
				{
					Limit:       2,
					Sort:        "by_name",
					UsePrevP:    true,
					WantUserIDs: []string{charlie, doris},
					// pin the next page based on the state at since
					SinceRequestIndex: 0,
				},
				{
					Limit:       2,
					Sort:        "by_name",
					UsePrevP:    true,
					WantUserIDs: []string{eve},
					// pin the next page based on the state at since
					SinceRequestIndex: 0,
				},
			},
		},
		{
			Name:    "Getting chronological deltas with a since token works",
			Creator: bob,
			TimelineMemberLog: []memlog{
				{
					Sender:     alice,
					Target:     alice,
					Membership: "join",
				},
			},
			Requests: []memrequest{
				{
					// default limit should be >2 and sort order should be by PL then name so bob first
					// as he made the room
					WantUserIDs:       []string{bob, alice},
					SinceRequestIndex: EmptySince,
				},
				{
					SinceRequestIndex: 0,
					InjectMembersBeforeRequest: []memlog{
						{
							Sender:     charlie,
							Target:     charlie,
							Membership: "join",
						},
					},
					WantUserIDs: []string{charlie},
				},
				{
					SinceRequestIndex: 1,
					InjectMembersBeforeRequest: []memlog{
						{
							Sender:     eve,
							Target:     eve,
							Membership: "join",
						},
						{
							Sender:     doris,
							Target:     doris,
							Membership: "join",
						},
					},
					// should be in time order not ordered by name
					WantUserIDs: []string{eve, doris},
				},
			},
		},
		{
			Name:    "sorting by PL/name works",
			Creator: alice,
			Names: map[string]string{
				alice:   "ZAlice",
				charlie: "YCharlie",
			},
			TimelineMemberLog: []memlog{
				{
					Sender:     bob,
					Target:     bob,
					Membership: "join",
				},
				{
					Sender:     charlie,
					Target:     charlie,
					Membership: "join",
				},
			},
			Requests: []memrequest{
				{
					// @bob:localhost, YCharlie, ZAlice
					Sort:              "by_name",
					SinceRequestIndex: EmptySince,
					WantUserIDs:       []string{bob, charlie, alice},
				},
				{
					// Alice made the room so has the most power, then @bob:localhost, YCharlie
					Sort:              "by_pl",
					SinceRequestIndex: EmptySince,
					WantUserIDs:       []string{alice, bob, charlie},
				},
			},
		},
		{
			Name:    "If the state changes from underneath the client whilst paginating, everything still works",
			Creator: alice,
			StateMemberLog: []memlog{
				{
					Sender:     eve,
					Target:     eve,
					Membership: "join",
				},
				{
					Sender:     charlie,
					Target:     charlie,
					Membership: "join",
				},
			},
			TimelineMemberLog: []memlog{
				{
					Sender:     doris,
					Target:     doris,
					Membership: "join",
				},
				{
					Sender:     bob,
					Target:     bob,
					Membership: "join",
				},
			},
			Requests: []memrequest{
				{
					SinceRequestIndex: EmptySince,
					Limit:             4,
					Sort:              "by_name",
					WantUserIDs:       []string{alice, bob, charlie, doris},
				},
				{
					// injecting this member should not cause it to be returned in pagination mode.
					InjectMembersBeforeRequest: []memlog{
						{
							Sender:     frank,
							Target:     frank,
							Membership: "join",
						},
					},
					Limit:             4,
					Sort:              "by_name",
					UsePrevP:          true,
					SinceRequestIndex: 0, // we need to stay on the same state as the previous request
					WantUserIDs:       []string{eve},
				},
			},
		},
		{
			Name:    "'limit' on streaming requests works",
			Creator: alice,
			TimelineMemberLog: []memlog{
				{
					Sender:     eve,
					Target:     eve,
					Membership: "join",
				},
			},
			Requests: []memrequest{
				{
					Limit:             2,
					WantUserIDs:       []string{alice, eve},
					SinceRequestIndex: EmptySince,
				},
				{
					InjectMembersBeforeRequest: []memlog{
						{
							Sender:     charlie,
							Target:     charlie,
							Membership: "join",
						},
						{
							Sender:     doris,
							Target:     doris,
							Membership: "join",
						},
						{
							Sender:     bob,
							Target:     bob,
							Membership: "join",
						},
					},
					Limit:             2,
					WantUserIDs:       []string{charlie, doris},
					SinceRequestIndex: 0,
				},
				{
					Limit:             2,
					WantUserIDs:       []string{bob},
					SinceRequestIndex: 1,
				},
			},
		},
	}
	roomIndex := 0
	for _, tc := range testCases {
		if t.Failed() {
			break
		}
		t.Run(tc.Name, func(t *testing.T) {
			// make the v2 sync responses based on test case info
			roomIndex++
			roomID := fmt.Sprintf("!%d:localhost", roomIndex)
			creatorMemberContent := map[string]interface{}{
				"membership": "join",
			}
			if tc.Names[tc.Creator] != "" {
				creatorMemberContent["displayname"] = tc.Names[tc.Creator]
			}
			if tc.PL == nil {
				var pl gomatrixserverlib.PowerLevelContent
				pl.Defaults()
				pl.Users = map[string]int64{tc.Creator: 100}
				tc.PL = &pl
			}
			state := []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.create", "", tc.Creator, map[string]interface{}{
					"creator": tc.Creator,
				}),
				testutils.NewStateEvent(t, "m.room.member", tc.Creator, tc.Creator, creatorMemberContent),
				testutils.NewStateEvent(t, "m.room.power_levels", "", tc.Creator, tc.PL),
			}
			for _, ml := range tc.StateMemberLog {
				content := map[string]interface{}{
					"membership": ml.Membership,
				}
				if ml.Membership == "join" && tc.Names[ml.Target] != "" {
					content["displayname"] = tc.Names[ml.Target]
				}
				state = append(state, testutils.NewStateEvent(t, "m.room.member", ml.Target, ml.Sender, content))
			}
			var timeline []json.RawMessage
			for _, ml := range tc.TimelineMemberLog {
				content := map[string]interface{}{
					"membership": ml.Membership,
				}
				if ml.Membership == "join" && tc.Names[ml.Target] != "" {
					content["displayname"] = tc.Names[ml.Target]
				}
				timeline = append(timeline, testutils.NewStateEvent(t, "m.room.member", ml.Target, ml.Sender, content))
			}
			if len(timeline) == 0 {
				t.Fatalf("test must have timeline entries in order to update upcoming token")
			}
			var v2Resp sync2.SyncResponse
			var jr sync2.SyncV2JoinResponse
			jr.State.Events = state
			jr.Timeline.Events = timeline
			v2Resp.Rooms.Join = make(map[string]sync2.SyncV2JoinResponse)
			v2Resp.Rooms.Join[roomID] = jr
			t.Logf("v2 for %v injecting %v state and %v timeline", roomID, len(state), len(timeline))
			aliceV2Stream(&v2Resp)()

			// run the requests
			var nextBatches []string
			prevP := ""
			for _, req := range tc.Requests {
				if req.InjectMembersBeforeRequest != nil {
					var timeline []json.RawMessage
					for _, ml := range req.InjectMembersBeforeRequest {
						content := map[string]interface{}{
							"membership": ml.Membership,
						}
						if ml.Membership == "join" && tc.Names[ml.Target] != "" {
							content["displayname"] = tc.Names[ml.Target]
						}
						timeline = append(timeline, testutils.NewStateEvent(t, "m.room.member", ml.Target, ml.Sender, content))
					}
					var v2Resp sync2.SyncResponse
					var jr sync2.SyncV2JoinResponse
					jr.Timeline.Events = timeline
					v2Resp.Rooms.Join = make(map[string]sync2.SyncV2JoinResponse)
					v2Resp.Rooms.Join[roomID] = jr
					aliceV2Stream(&v2Resp)()
				}

				filter := map[string]interface{}{
					"room_id": roomID,
				}
				if req.Limit > 0 {
					filter["limit"] = req.Limit
				}
				if req.Sort != "" {
					filter["sort"] = req.Sort
				}
				since := ""
				if req.SinceRequestIndex != EmptySince {
					since = nextBatches[req.SinceRequestIndex]
				}
				if req.UsePrevP {
					filter["next_page"] = prevP
				}
				t.Logf("Room member since=%v request: %+v", since, filter)
				v3resp := mustDoSync3Request(t, server, aliceBearer, since, map[string]interface{}{
					"room_member": filter,
				})
				nextBatches = append(nextBatches, v3resp.Next)
				if v3resp.RoomMember == nil {
					t.Fatalf("response did not include room_member: test case %v", tc.Name)
				}
				if v3resp.RoomMember.NextPage != "" {
					prevP = v3resp.RoomMember.NextPage
				}
				for _, ev := range v3resp.RoomMember.Events {
					t.Logf("%s", string(ev))
				}
				t.Logf("want: %v", req.WantUserIDs)
				if req.WantUserIDs != nil {
					if len(v3resp.RoomMember.Events) != len(req.WantUserIDs) {
						t.Fatalf("got %d room members, want %d - test case: %v", len(v3resp.RoomMember.Events), len(req.WantUserIDs), tc.Name)
					}
					for i := range req.WantUserIDs {
						gotUserID := gjson.GetBytes(v3resp.RoomMember.Events[i], "state_key").Str
						if gotUserID != req.WantUserIDs[i] {
							t.Errorf("position %d got %v want %v - test case: %v", i, gotUserID, req.WantUserIDs[i], tc.Name)
						}
					}
				}
			}
		})
	}
}
