package syncv3

import (
	"encoding/json"
	"testing"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/sync3/extensions"
	"github.com/matrix-org/sync-v3/testutils"
)

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
			Rooms: sync3.SliceRanges{
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
			Rooms: sync3.SliceRanges{
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
			Rooms: sync3.SliceRanges{
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
			Rooms: sync3.SliceRanges{
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
			Rooms: sync3.SliceRanges{
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
