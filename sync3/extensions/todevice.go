package extensions

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/matrix-org/sync-v3/state"
)

// Client created request params
type ToDeviceRequest struct {
	Enabled *bool  `json:"enabled"`
	Limit   int    `json:"limit"` // max number of to-device messages per response
	Since   string `json:"since"` // since token
}

func (r ToDeviceRequest) ApplyDelta(next *ToDeviceRequest) *ToDeviceRequest {
	if next.Enabled != nil {
		r.Enabled = next.Enabled
	}
	if next.Limit != 0 {
		r.Limit = next.Limit
	}
	if next.Since != "" {
		r.Since = next.Since
	}
	return &r
}

// Server response
type ToDeviceResponse struct {
	NextBatch string            `json:"next_batch"`
	Events    []json.RawMessage `json:"events,omitempty"`
}

func (r *ToDeviceResponse) HasData(isInitial bool) bool {
	return len(r.Events) > 0
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
		// the client is confirming messages up to `from` so delete everything up to and including it.
		if err = store.ToDeviceTable.DeleteMessagesUpToAndIncluding(deviceID, from); err != nil {
			l.Err(err).Str("since", req.Since).Msg("failed to delete to-device messages up to this value")
			// non-fatal
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
	return
}
