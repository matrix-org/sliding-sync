package syncv3

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/sync3/extensions"
	"github.com/matrix-org/sync-v3/testutils"
)

var valTrue = true

// Checks that e2ee v2 sections `device_lists` and `device_one_time_keys_count` are passed to v3
func TestExtensionE2EE(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	// check that OTK counts go through
	otkCounts := map[string]int{
		"curve25519":        10,
		"signed_curve25519": 100,
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		DeviceListsOTKCount: otkCounts,
	})
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		// enable the E2EE extension
		Extensions: extensions.Request{
			E2EE: &extensions.E2EERequest{
				Enabled: true,
			},
		},
	})
	MatchResponse(t, res, MatchOTKCounts(otkCounts))

	// check that OTK counts remain constant when they aren't included in the v2 response.
	// Do this by feeding in a new joined room
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: "!doesnt-matter",
				name:   "Poke",
				events: createRoomState(t, alice, time.Now()),
			}),
		},
	})
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		// skip enabled: true as it should be sticky
	})
	MatchResponse(t, res, MatchOTKCounts(otkCounts))

	// check that OTK counts update when they are included in the v2 response
	otkCounts = map[string]int{
		"curve25519":        99,
		"signed_curve25519": 999,
	}
	v2.queueResponse(alice, sync2.SyncResponse{
		DeviceListsOTKCount: otkCounts,
	})
	v2.waitUntilEmpty(t, alice)
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		// enable the E2EE extension
		Extensions: extensions.Request{
			E2EE: &extensions.E2EERequest{
				Enabled: true,
			},
		},
	})
	MatchResponse(t, res, MatchOTKCounts(otkCounts))

	// check that changed|left get passed to v3
	wantChanged := []string{"bob"}
	wantLeft := []string{"charlie"}
	v2.queueResponse(alice, sync2.SyncResponse{
		DeviceLists: struct {
			Changed []string `json:"changed,omitempty"`
			Left    []string `json:"left,omitempty"`
		}{
			Changed: wantChanged,
			Left:    wantLeft,
		},
	})
	v2.waitUntilEmpty(t, alice)
	lastPos := res.Pos
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		// enable the E2EE extension
		Extensions: extensions.Request{
			E2EE: &extensions.E2EERequest{
				Enabled: true,
			},
		},
	})
	MatchResponse(t, res, MatchDeviceLists(wantChanged, wantLeft))

	// check that changed|left persist if requesting with the same v3 position
	res = v3.mustDoV3RequestWithPos(t, aliceToken, lastPos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		// enable the E2EE extension
		Extensions: extensions.Request{
			E2EE: &extensions.E2EERequest{
				Enabled: true,
			},
		},
	})
	MatchResponse(t, res, MatchDeviceLists(wantChanged, wantLeft))

	// check that changed|left do *not* persist once consumed (advanced v3 position). This requires
	// another poke so we don't wait until up to the timeout value in tests
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: "!doesnt-matter2",
				name:   "Poke 2",
				events: createRoomState(t, alice, time.Now()),
			}),
		},
	})
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		// enable the E2EE extension
		Extensions: extensions.Request{
			E2EE: &extensions.E2EERequest{
				Enabled: true,
			},
		},
	})
	MatchResponse(t, res, func(res *sync3.Response) error {
		if res.Extensions.E2EE.DeviceLists != nil {
			return fmt.Errorf("e2ee device lists present when it shouldn't be")
		}
		return nil
	})
}

// Checks that to-device messages are passed from v2 to v3
// 1: check that a fresh sync returns to-device messages
// 2: repeating the fresh sync request returns the same messages (not deleted)
// 3: update the since token -> no new messages
// 4: inject live to-device messages -> receive them only.
// 5: repeating the previous sync request returns the same live to-device messages (retransmit)
// 6: using an old since token does not return to-device messages anymore as they were deleted.
func TestExtensionToDevice(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	alice := "@TestExtensionToDevice_alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN_TestExtensionToDevice"
	v2.addAccount(alice, aliceToken)
	toDeviceMsgs := []json.RawMessage{
		json.RawMessage(`{"sender":"alice","type":"something","content":{"foo":"1"}}`),
		json.RawMessage(`{"sender":"alice","type":"something","content":{"foo":"2"}}`),
		json.RawMessage(`{"sender":"alice","type":"something","content":{"foo":"3"}}`),
		json.RawMessage(`{"sender":"alice","type":"something","content":{"foo":"4"}}`),
	}
	v2.queueResponse(alice, sync2.SyncResponse{
		ToDevice: sync2.EventsResponse{
			Events: toDeviceMsgs,
		},
	})

	// 1: check that a fresh sync returns to-device messages
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Enabled: &valTrue,
			},
		},
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages(toDeviceMsgs))

	// 2: repeating the fresh sync request returns the same messages (not deleted)
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Enabled: &valTrue,
			},
		},
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages(toDeviceMsgs))

	// 3: update the since token -> no new messages
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Enabled: &valTrue,
				Since:   res.Extensions.ToDevice.NextBatch,
			},
		},
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages([]json.RawMessage{}))

	// 4: inject live to-device messages -> receive them only.
	sinceBeforeMsgs := res.Extensions.ToDevice.NextBatch
	newToDeviceMsgs := []json.RawMessage{
		json.RawMessage(`{"sender":"alice","type":"something","content":{"foo":"5"}}`),
		json.RawMessage(`{"sender":"alice","type":"something","content":{"foo":"6"}}`),
	}
	v2.queueResponse(alice, sync2.SyncResponse{
		ToDevice: sync2.EventsResponse{
			Events: newToDeviceMsgs,
		},
	})
	v2.waitUntilEmpty(t, alice)
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Since: sinceBeforeMsgs,
			},
		},
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages(newToDeviceMsgs))

	// 5: repeating the previous sync request returns the same live to-device messages (retransmit)
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Since: sinceBeforeMsgs,
			},
		},
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages(newToDeviceMsgs))

	// ack the to-device messages
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Since: res.Extensions.ToDevice.NextBatch,
			},
		},
	})
	// this response contains nothing
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages([]json.RawMessage{}))

	// 6: using an old since token does not return to-device messages anymore as they were deleted.
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Since: sinceBeforeMsgs,
			},
		},
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages([]json.RawMessage{}))
}

// tests that the account data extension works:
// 1- check global account data is sent on first connection
// 2- check global account data updates are proxied through
// 3- check room account data for the list only is sent
// 4- check room account data for subscriptions are sent
// 5- when the range changes, make sure room account data is sent
// 6- when a room bumps into a range, make sure room account data is sent
func TestExtensionAccountData(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	alice := "@alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN"
	roomA := "!a:localhost"
	roomB := "!b:localhost"
	roomC := "!c:localhost"
	globalAccountData := []json.RawMessage{
		testutils.NewAccountData(t, "im-global", map[string]interface{}{"body": "yep"}),
		testutils.NewAccountData(t, "im-also-global", map[string]interface{}{"body": "yep"}),
	}
	roomAAccountData := []json.RawMessage{
		testutils.NewAccountData(t, "im-a", map[string]interface{}{"body": "yep a"}),
		testutils.NewAccountData(t, "im-also-a", map[string]interface{}{"body": "yep A"}),
	}
	roomBAccountData := []json.RawMessage{
		testutils.NewAccountData(t, "im-b", map[string]interface{}{"body": "yep b"}),
		testutils.NewAccountData(t, "im-also-b", map[string]interface{}{"body": "yep B"}),
	}
	roomCAccountData := []json.RawMessage{
		testutils.NewAccountData(t, "im-c", map[string]interface{}{"body": "yep c"}),
		testutils.NewAccountData(t, "im-also-c", map[string]interface{}{"body": "yep C"}),
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		AccountData: sync2.EventsResponse{
			Events: globalAccountData,
		},
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				roomA: {
					State: sync2.EventsResponse{
						Events: createRoomState(t, alice, time.Now()),
					},
					AccountData: sync2.EventsResponse{
						Events: roomAAccountData,
					},
				},
				roomB: {
					State: sync2.EventsResponse{
						Events: createRoomState(t, alice, time.Now().Add(-1*time.Minute)),
					},
					AccountData: sync2.EventsResponse{
						Events: roomBAccountData,
					},
				},
				roomC: {
					State: sync2.EventsResponse{
						Events: createRoomState(t, alice, time.Now().Add(-2*time.Minute)),
					},
					AccountData: sync2.EventsResponse{
						Events: roomCAccountData,
					},
				},
			},
		},
	})

	// 1- check global account data is sent on first connection
	// 3- check room account data for the list only is sent
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Extensions: extensions.Request{
			AccountData: &extensions.AccountDataRequest{
				Enabled: true,
			},
		},
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 1}, // first two rooms A,B
			},
			Sort: []string{sync3.SortByRecency},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 0,
			},
		}},
	})
	MatchResponse(t, res, MatchAccountData(
		globalAccountData,
		map[string][]json.RawMessage{
			roomA: roomAAccountData,
			roomB: roomBAccountData,
		},
	))

	// 5- when the range changes, make sure room account data is sent
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 2}, // A,B,C
			},
		}},
	})
	MatchResponse(t, res, MatchAccountData(
		nil,
		map[string][]json.RawMessage{
			roomC: roomCAccountData,
		},
	))

	// 4- check room account data for subscriptions are sent
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Extensions: extensions.Request{
			AccountData: &extensions.AccountDataRequest{
				Enabled: true,
			},
		},
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomB: {
				TimelineLimit: 1,
			},
		},
	})
	MatchResponse(t, res, MatchAccountData(
		globalAccountData,
		map[string][]json.RawMessage{
			roomB: roomBAccountData,
		},
	))

	// 2- check global account data updates are proxied through
	newGlobalEvent := testutils.NewAccountData(t, "new_fun_event", map[string]interface{}{"much": "excite"})
	v2.queueResponse(alice, sync2.SyncResponse{
		AccountData: sync2.EventsResponse{
			Events: []json.RawMessage{newGlobalEvent},
		},
	})
	v2.waitUntilEmpty(t, alice)
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{})
	MatchResponse(t, res, MatchAccountData(
		[]json.RawMessage{newGlobalEvent},
		nil,
	))

	// 6- when a room bumps into a range, make sure room account data is sent
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Extensions: extensions.Request{
			AccountData: &extensions.AccountDataRequest{
				Enabled: true,
			},
		},
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 1}, // first two rooms A,B
			},
			Sort: []string{sync3.SortByRecency},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 0,
			},
		}},
	})
	// bump C to position 0
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomC,
				events: []json.RawMessage{
					testutils.NewEvent(
						t, "m.poke", alice, map[string]interface{}{},
						testutils.WithTimestamp(time.Now().Add(time.Millisecond)),
					),
				},
			}),
		},
	})
	v2.waitUntilEmpty(t, alice)
	// now we should get room account data for C
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 1}, // first two rooms A,B
			},
		}},
	})
	MatchResponse(t, res, MatchAccountData(
		nil,
		map[string][]json.RawMessage{
			roomC: roomCAccountData,
		},
	))
}
