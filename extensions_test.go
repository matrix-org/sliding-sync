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

// Checks that e2ee v2 sections `device_lists` and `device_one_time_keys_count` are passed to v3
func TestExtensionE2EE(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	alice := "@TestExtensionE2EE_alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN_TestExtensionE2EE"
	sessionID := "sid"

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
			E2EE: extensions.E2EERequest{
				Enabled: true,
			},
		},
		SessionID: sessionID,
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
		// enable the E2EE extension
		Extensions: extensions.Request{
			E2EE: extensions.E2EERequest{
				Enabled: true,
			},
		},
		SessionID: sessionID,
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
			E2EE: extensions.E2EERequest{
				Enabled: true,
			},
		},
		SessionID: sessionID,
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
			E2EE: extensions.E2EERequest{
				Enabled: true,
			},
		},
		SessionID: sessionID,
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
			E2EE: extensions.E2EERequest{
				Enabled: true,
			},
		},
		SessionID: sessionID,
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
			E2EE: extensions.E2EERequest{
				Enabled: true,
			},
		},
		SessionID: sessionID,
	})
	MatchResponse(t, res, func(res *sync3.Response) error {
		if res.Extensions.E2EE.DeviceLists != nil {
			return fmt.Errorf("e2ee device lists present when it shouldn't be")
		}
		return nil
	})
}

// Checks that to-device messages are passed from v2 to v3
func TestExtensionToDevice(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	alice := "@TestExtensionToDevice_alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN_TestExtensionToDevice"
	sessionID := "sid"
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

	// query to-device messages -> get all of them
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: extensions.ToDeviceRequest{
				Enabled: true,
			},
		},
		SessionID: sessionID,
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages(toDeviceMsgs))

	// repeat request -> get all of them
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: extensions.ToDeviceRequest{
				Enabled: true,
			},
		},
		SessionID: sessionID,
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages(toDeviceMsgs))

	// update the since token -> don't get messages again
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: extensions.ToDeviceRequest{
				Enabled: true,
				Since:   res.Extensions.ToDevice.NextBatch,
			},
		},
		SessionID: sessionID,
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages([]json.RawMessage{}))

	// add new to-device messages, ensure we get them
	newToDeviceMsgs := []json.RawMessage{
		json.RawMessage(`{"sender":"alice","type":"something","content":{"foo":"5"}}`),
		json.RawMessage(`{"sender":"alice","type":"something","content":{"foo":"6"}}`),
	}
	v2.queueResponse(alice, sync2.SyncResponse{
		ToDevice: sync2.EventsResponse{
			Events: newToDeviceMsgs,
		},
	})
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: extensions.ToDeviceRequest{
				Enabled: true,
				Since:   res.Extensions.ToDevice.NextBatch,
			},
		},
		SessionID: sessionID,
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages(newToDeviceMsgs))

	// update the since token -> don't get new ones again
	res = v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{{
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10}, // doesn't matter
			},
		}},
		Extensions: extensions.Request{
			ToDevice: extensions.ToDeviceRequest{
				Enabled: true,
				Since:   res.Extensions.ToDevice.NextBatch,
			},
		},
		SessionID: sessionID,
	})
	MatchResponse(t, res, MatchV3Count(0), MatchToDeviceMessages([]json.RawMessage{}))

	// TODO: roll back the since token -> don't get messages again as they were deleted
	// - do we need sessions at all? Can we delete if the since value is incremented?
	// - check with ios folks if this level of co-ordination between processes is possible.
}
