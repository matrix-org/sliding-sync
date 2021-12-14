package extensions

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/matrix-org/sync-v3/state"
)

// Client created request params
type ToDeviceRequest struct {
	Enabled bool   `json:"enabled"`
	Limit   int    `json:"limit"` // max number of to-device messages per response
	Since   string `json:"since"` // since token
}

// Server response
type ToDeviceResponse struct {
	NextBatch string            `json:"next_batch"`
	Events    []json.RawMessage `json:"events,omitempty"`
}

func ProcessToDevice(store *state.Storage, userID, deviceID string, req *ToDeviceRequest) (res *ToDeviceResponse) {
	if req.Limit == 0 {
		req.Limit = 100 // default to 100
	}
	l := logger.With().Str("user", userID).Str("device", deviceID).Logger()
	var from int64
	var err error
	if req.Since != "" {
		from, err = strconv.ParseInt(req.Since, 10, 64)
		if err != nil {
			l.Err(err).Str("since", req.Since).Msg("invalid since value")
			return nil
		}
	}

	msgs, upTo, err := store.ToDeviceTable.Messages(deviceID, from, -1, int64(req.Limit))
	if err != nil {
		l.Err(err).Int64("from", from).Msg("cannot query to-device messages")
		return nil
	}
	res = &ToDeviceResponse{
		NextBatch: fmt.Sprintf("%d", upTo),
		Events:    msgs,
	}

	// Make H use since tokens correctly
	// TEST: it probably won't work due to OTK counts / device lists:
	/*
			{
		  "next_batch": "s72595_4483_1934",
		  "rooms": {"leave": {}, "join": {}, "invite": {}},
		  "device_lists": {
		    "changed": [
		       "@alice:example.com",
		    ],
		    "left": [
		       "@bob:example.com",
		    ],
		  },
		  "device_one_time_keys_count": {
		    "curve25519": 10,
		    "signed_curve25519": 20
		  }
		}
	*/
	// need to persist OTK counts (like unread notifications, clobber)
	// need to persist device lists (how?)
	return
}
