package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/matrix-org/sliding-sync/internal"

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
	Core
	Limit int    `json:"limit"` // max number of to-device messages per response
	Since string `json:"since"` // since token
}

func (r *ToDeviceRequest) Name() string {
	return "ToDeviceRequest"
}

func (r *ToDeviceRequest) ApplyDelta(gnext GenericRequest) {
	r.Core.ApplyDelta(gnext)
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

func (r *ToDeviceRequest) AppendLive(ctx context.Context, res *Response, extCtx Context, up caches.Update) {
	_, ok := up.(caches.DeviceEventsUpdate)
	if !ok {
		return
	}
	// DeviceEventsUpdate has no data and just serves to poke this extension to recheck the database
	r.ProcessInitial(ctx, res, extCtx)
}

func (r *ToDeviceRequest) ProcessInitial(ctx context.Context, res *Response, extCtx Context) {
	if r.Limit == 0 {
		r.Limit = 100 // default to 100
	}
	l := logger.With().Str("user", extCtx.UserID).Str("device", extCtx.DeviceID).Logger()
	var from int64
	var err error
	if r.Since != "" {
		from, err = strconv.ParseInt(r.Since, 10, 64)
		if err != nil {
			l.Err(err).Str("since", r.Since).Msg("invalid since value")
			// TODO add context to sentry
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
			return
		}
		// the client is confirming messages up to `from` so delete everything up to and including it.
		if err = extCtx.Store.ToDeviceTable.DeleteMessagesUpToAndIncluding(extCtx.UserID, extCtx.DeviceID, from); err != nil {
			l.Err(err).Str("since", r.Since).Msg("failed to delete to-device messages up to this value")
			// TODO add context to sentry
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		}
	}
	mapMu.Lock()
	lastSentPos := deviceIDToSinceDebugOnly[extCtx.DeviceID]
	internal.Logf(ctx, "to_device", "since=%v limit=%v last_sent=%v", r.Since, r.Limit, lastSentPos)
	mapMu.Unlock()
	if from < lastSentPos {
		// we told the client about a newer position, but yet they are using an older position, yell loudly
		l.Warn().Int64("last_sent", lastSentPos).Int64("recv", from).Bool("initial", extCtx.IsInitial).Msg(
			"Client did not increment since token: possibly sending back duplicate to-device events!",
		)
	}

	msgs, upTo, err := extCtx.Store.ToDeviceTable.Messages(extCtx.UserID, extCtx.DeviceID, from, int64(r.Limit))
	if err != nil {
		l.Err(err).Int64("from", from).Msg("cannot query to-device messages")
		// TODO add context to sentry
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return
	}
	err = extCtx.Store.ToDeviceTable.SetUnackedPosition(extCtx.UserID, extCtx.DeviceID, upTo)
	if err != nil {
		l.Err(err).Msg("cannot set unacked position")
		// TODO add context to sentry
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return
	}
	mapMu.Lock()
	deviceIDToSinceDebugOnly[extCtx.DeviceID] = upTo
	mapMu.Unlock()
	// we don't need to aggregate here as we're pulling from the DB and not relying on in-memory structs
	res.ToDevice = &ToDeviceResponse{
		NextBatch: fmt.Sprintf("%d", upTo),
		Events:    msgs,
	}
}
