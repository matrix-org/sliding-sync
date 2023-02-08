package extensions

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

// used to remember since positions to warn when they are not incremented. This can happen
// when the response is genuinely lost, but then we would expect the Conn cache to pick it up based
// on resending the `?pos=`, so it could mean they really aren't incrementing it and it's a client bug,
// which is bad because it can result in duplicate to-device events which breaks the encryption state machine
var deviceIDToSinceDebugOnly = map[string]int64{}
var mapMu = &sync.Mutex{}

// Client created request params
type ToDeviceRequest struct {
	Enableable
	Limit int    `json:"limit"` // max number of to-device messages per response
	Since string `json:"since"` // since token
}

func (r *ToDeviceRequest) Name() string {
	return "ToDeviceRequest"
}

func (r *ToDeviceRequest) ApplyDelta(gnext GenericRequest) {
	r.Enableable.ApplyDelta(gnext)
	next := gnext.(*ToDeviceRequest)
	if next.Limit != 0 {
		r.Limit = next.Limit
	}
	if next.Since != "" {
		r.Since = next.Since
	}
}

// Server response
type ToDeviceResponse struct {
	NextBatch string            `json:"next_batch"`
	Events    []json.RawMessage `json:"events,omitempty"`
}

func (r *ToDeviceResponse) HasData(isInitial bool) bool {
	return len(r.Events) > 0
}

func ProcessLiveToDeviceEvents(up caches.Update, store *state.Storage, userID, deviceID string, req *ToDeviceRequest) (res *ToDeviceResponse) {
	_, ok := up.(caches.DeviceEventsUpdate)
	if !ok {
		return nil
	}
	return ProcessToDevice(store, userID, deviceID, req, false)
}

func ProcessToDevice(store *state.Storage, userID, deviceID string, req *ToDeviceRequest, isInitial bool) (res *ToDeviceResponse) {
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
			// non-fatal TODO sentry
		}
	}
	mapMu.Lock()
	lastSentPos := deviceIDToSinceDebugOnly[deviceID]
	mapMu.Unlock()
	if from < lastSentPos {
		// we told the client about a newer position, but yet they are using an older position, yell loudly
		// TODO sentry
		l.Warn().Int64("last_sent", lastSentPos).Int64("recv", from).Bool("initial", isInitial).Msg(
			"Client did not increment since token: possibly sending back duplicate to-device events!",
		)
	}

	msgs, upTo, err := store.ToDeviceTable.Messages(deviceID, from, int64(req.Limit))
	if err != nil {
		l.Err(err).Int64("from", from).Msg("cannot query to-device messages")
		// TODO sentry
		return nil
	}
	err = store.ToDeviceTable.SetUnackedPosition(deviceID, upTo)
	if err != nil {
		l.Err(err).Msg("cannot set unacked position")
		// TODO sentry
		return nil
	}
	mapMu.Lock()
	deviceIDToSinceDebugOnly[deviceID] = upTo
	mapMu.Unlock()
	res = &ToDeviceResponse{
		NextBatch: fmt.Sprintf("%d", upTo),
		Events:    msgs,
	}
	return
}
