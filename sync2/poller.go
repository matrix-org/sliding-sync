package sync2

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/getsentry/sentry-go"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

type PollerID struct {
	UserID   string
	DeviceID string
}

// alias time.Sleep so tests can monkey patch it out
var timeSleep = time.Sleep

// V2DataReceiver is the receiver for all the v2 sync data the poller gets
type V2DataReceiver interface {
	// Update the since token for this device. Called AFTER all other data in this sync response has been processed.
	UpdateDeviceSince(ctx context.Context, userID, deviceID, since string)
	// Accumulate data for this room. This means the timeline section of the v2 response.
	Accumulate(ctx context.Context, userID, deviceID, roomID, prevBatch string, timeline []json.RawMessage) // latest pos with event nids of timeline entries
	// Initialise the room, if it hasn't been already. This means the state section of the v2 response.
	// If given a state delta from an incremental sync, returns the slice of all state events unknown to the DB.
	Initialise(ctx context.Context, roomID string, state []json.RawMessage) []json.RawMessage // snapshot ID?
	// SetTyping indicates which users are typing.
	SetTyping(ctx context.Context, roomID string, ephEvent json.RawMessage)
	// Sent when there is a new receipt
	OnReceipt(ctx context.Context, userID, roomID, ephEventType string, ephEvent json.RawMessage)
	// AddToDeviceMessages adds this chunk of to_device messages. Preserve the ordering.
	AddToDeviceMessages(ctx context.Context, userID, deviceID string, msgs []json.RawMessage) // start/end stream pos
	// UpdateUnreadCounts sets the highlight_count and notification_count for this user in this room.
	UpdateUnreadCounts(ctx context.Context, roomID, userID string, highlightCount, notifCount *int)
	// Set the latest account data for this user.
	OnAccountData(ctx context.Context, userID, roomID string, events []json.RawMessage) // ping update with types? Can you race when re-querying?
	// Sent when there is a room in the `invite` section of the v2 response.
	OnInvite(ctx context.Context, userID, roomID string, inviteState []json.RawMessage) // invitestate in db
	// Sent when there is a room in the `leave` section of the v2 response.
	OnLeftRoom(ctx context.Context, userID, roomID string)
	// Sent when there is a _change_ in E2EE data, not all the time
	OnE2EEData(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int)
	// Sent when the poll loop terminates
	OnTerminated(ctx context.Context, userID, deviceID string)
	// Sent when the token gets a 401 response
	OnExpiredToken(ctx context.Context, accessTokenHash, userID, deviceID string)
}

// PollerMap is a map of device ID to Poller
type PollerMap struct {
	v2Client                 Client
	callbacks                V2DataReceiver
	pollerMu                 *sync.Mutex
	Pollers                  map[PollerID]*poller
	executor                 chan func()
	executorRunning          bool
	pollHistogramVec         *prometheus.HistogramVec
	processHistogramVec      *prometheus.HistogramVec
	timelineSizeHistogramVec *prometheus.HistogramVec
}

// NewPollerMap makes a new PollerMap. Guarantees that the V2DataReceiver will be called on the same
// goroutine for all pollers. This is required to avoid race conditions at the Go level. Whilst we
// use SQL transactions to ensure that the DB doesn't race, we then subsequently feed new events
// from that call into a global cache. This can race which can result in out of order latest NIDs
// which, if we assert NIDs only increment, will result in missed events.
//
// Consider these events in the same room, with 3 different pollers getting the data:
//
//	1 2 3 4 5 6 7 eventual DB event NID
//	A B C D E F G
//	-----          poll loop 1 = A,B,C          new events = A,B,C latest=3
//	---------      poll loop 2 = A,B,C,D,E      new events = D,E   latest=5
//	-------------  poll loop 3 = A,B,C,D,E,F,G  new events = F,G   latest=7
//
// The DB layer will correctly assign NIDs and stop duplicates, resulting in a set of new events which
// do not overlap. However, there is a gap between this point and updating the cache, where variable
// delays can be introduced, so F,G latest=7 could be injected first. If we then never walk back to
// earlier NIDs, A,B,C,D,E will be dropped from the cache.
//
// This only affects resources which are shared across multiple DEVICES such as:
//   - room resources: events, EDUs
//   - user resources: notif counts, account data
//
// NOT to-device messages,or since tokens.
func NewPollerMap(v2Client Client, enablePrometheus bool) *PollerMap {
	pm := &PollerMap{
		v2Client: v2Client,
		pollerMu: &sync.Mutex{},
		Pollers:  make(map[PollerID]*poller),
		executor: make(chan func(), 0),
	}
	if enablePrometheus {
		pm.pollHistogramVec = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "sliding_sync",
			Subsystem: "poller",
			Name:      "request_duration_secs",
			Help:      "Time taken in seconds for the sync v2 response to be received",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"initial", "first"})
		prometheus.MustRegister(pm.pollHistogramVec)
		pm.processHistogramVec = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "sliding_sync",
			Subsystem: "poller",
			Name:      "process_duration_secs",
			Help:      "Time taken in seconds for the sync v2 response to be processed fully",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"initial", "first"})
		prometheus.MustRegister(pm.processHistogramVec)
		pm.timelineSizeHistogramVec = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "sliding_sync",
			Subsystem: "poller",
			Name:      "timeline_size",
			Help:      "Number of events seen by the poller in a sync v2 timeline response",
			Buckets:   []float64{0.0, 1.0, 2.0, 5.0, 10.0, 20.0, 50.0},
		}, []string{"limited"})
		prometheus.MustRegister(pm.timelineSizeHistogramVec)

	}
	return pm
}

func (h *PollerMap) SetCallbacks(callbacks V2DataReceiver) {
	h.callbacks = callbacks
}

// Terminate all pollers. Useful in tests.
func (h *PollerMap) Terminate() {
	h.pollerMu.Lock()
	defer h.pollerMu.Unlock()
	for _, p := range h.Pollers {
		p.Terminate()
	}
	if h.pollHistogramVec != nil {
		prometheus.Unregister(h.pollHistogramVec)
	}
	if h.processHistogramVec != nil {
		prometheus.Unregister(h.processHistogramVec)
	}
	if h.timelineSizeHistogramVec != nil {
		prometheus.Unregister(h.timelineSizeHistogramVec)
	}
	close(h.executor)
}

func (h *PollerMap) NumPollers() (count int) {
	h.pollerMu.Lock()
	defer h.pollerMu.Unlock()
	for _, p := range h.Pollers {
		if !p.terminated.Load() {
			count++
		}
	}
	return
}

// EnsurePolling makes sure there is a poller for this device, making one if need be.
// Blocks until at least 1 sync is done if and only if the poller was just created.
// This ensures that calls to the database will return data.
// Guarantees only 1 poller will be running per deviceID.
// Note that we will immediately return if there is a poller for the same user but a different device.
// We do this to allow for logins on clients to be snappy fast, even though they won't yet have the
// to-device msgs to decrypt E2EE rooms.
func (h *PollerMap) EnsurePolling(pid PollerID, accessToken, v2since string, isStartup bool, logger zerolog.Logger) {
	h.pollerMu.Lock()
	if !h.executorRunning {
		h.executorRunning = true
		go h.execute()
	}
	poller, ok := h.Pollers[pid]
	// a poller exists and hasn't been terminated so we don't need to do anything
	if ok && !poller.terminated.Load() {
		if poller.accessToken != accessToken {
			logger.Warn().Msg("PollerMap.EnsurePolling: poller already running with different access token")
		}
		h.pollerMu.Unlock()
		// this existing poller may not have completed the initial sync yet, so we need to make sure
		// it has before we return.
		poller.WaitUntilInitialSync()
		return
	}
	// check if we need to wait at all: we don't need to if this user is already syncing on a different device
	// This is O(n) so we may want to map this if we get a lot of users...
	needToWait := true
	for existingPID, poller := range h.Pollers {
		// Ignore different users. Also ignore same-user same-device.
		if pid.UserID != existingPID.UserID || pid.DeviceID == existingPID.DeviceID {
			continue
		}
		// Now we have same-user different-device.
		if !poller.terminated.Load() {
			needToWait = false
			break
		}
	}

	// replace the poller. If we don't need to wait, then we just want to nab to-device events initially.
	// We don't do that on startup though as we cannot be sure that other pollers will not be using expired tokens.
	poller = newPoller(pid, accessToken, h.v2Client, h, logger, !needToWait && !isStartup)
	poller.pollHistogramVec = h.pollHistogramVec
	poller.processHistogramVec = h.processHistogramVec
	poller.timelineSizeVec = h.timelineSizeHistogramVec
	go poller.Poll(v2since)
	h.Pollers[pid] = poller

	h.pollerMu.Unlock()
	if needToWait {
		poller.WaitUntilInitialSync()
	} else {
		logger.Info().Str("user", poller.userID).Msg("a poller exists for this user; not waiting for this device to do an initial sync")
	}
}

func (h *PollerMap) execute() {
	for fn := range h.executor {
		fn()
	}
}

func (h *PollerMap) UpdateDeviceSince(ctx context.Context, userID, deviceID, since string) {
	h.callbacks.UpdateDeviceSince(ctx, userID, deviceID, since)
}
func (h *PollerMap) Accumulate(ctx context.Context, userID, deviceID, roomID, prevBatch string, timeline []json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.Accumulate(ctx, userID, deviceID, roomID, prevBatch, timeline)
		wg.Done()
	}
	wg.Wait()
}
func (h *PollerMap) Initialise(ctx context.Context, roomID string, state []json.RawMessage) (result []json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		result = h.callbacks.Initialise(ctx, roomID, state)
		wg.Done()
	}
	wg.Wait()
	return
}
func (h *PollerMap) SetTyping(ctx context.Context, roomID string, ephEvent json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.SetTyping(ctx, roomID, ephEvent)
		wg.Done()
	}
	wg.Wait()
}
func (h *PollerMap) OnInvite(ctx context.Context, userID, roomID string, inviteState []json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnInvite(ctx, userID, roomID, inviteState)
		wg.Done()
	}
	wg.Wait()
}

func (h *PollerMap) OnLeftRoom(ctx context.Context, userID, roomID string) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnLeftRoom(ctx, userID, roomID)
		wg.Done()
	}
	wg.Wait()
}

// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
// would implicitly acknowledge these messages.
func (h *PollerMap) AddToDeviceMessages(ctx context.Context, userID, deviceID string, msgs []json.RawMessage) {
	h.callbacks.AddToDeviceMessages(ctx, userID, deviceID, msgs)
}

func (h *PollerMap) OnTerminated(ctx context.Context, userID, deviceID string) {
	h.callbacks.OnTerminated(ctx, userID, deviceID)
}

func (h *PollerMap) OnExpiredToken(ctx context.Context, accessTokenHash, userID, deviceID string) {
	h.callbacks.OnExpiredToken(ctx, accessTokenHash, userID, deviceID)
}

func (h *PollerMap) UpdateUnreadCounts(ctx context.Context, roomID, userID string, highlightCount, notifCount *int) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.UpdateUnreadCounts(ctx, roomID, userID, highlightCount, notifCount)
		wg.Done()
	}
	wg.Wait()
}

func (h *PollerMap) OnAccountData(ctx context.Context, userID, roomID string, events []json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnAccountData(ctx, userID, roomID, events)
		wg.Done()
	}
	wg.Wait()
}

func (h *PollerMap) OnReceipt(ctx context.Context, userID, roomID, ephEventType string, ephEvent json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnReceipt(ctx, userID, roomID, ephEventType, ephEvent)
		wg.Done()
	}
	wg.Wait()
}

func (h *PollerMap) OnE2EEData(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) {
	// This is device-scoped data and will never race with another poller. Therefore we
	// do not need to queue this up in the executor. However: the poller does need to
	// wait for this to complete before advancing the since token, or else we risk
	// losing device list changes.
	h.callbacks.OnE2EEData(ctx, userID, deviceID, otkCounts, fallbackKeyTypes, deviceListChanges)
}

// Poller can automatically poll the sync v2 endpoint and accumulate the responses in storage
type poller struct {
	userID      string
	deviceID    string
	accessToken string
	client      Client
	receiver    V2DataReceiver
	logger      zerolog.Logger

	initialToDeviceOnly bool

	// E2EE fields: we keep them so we only send callbacks on deltas not all the time
	fallbackKeyTypes []string
	otkCounts        map[string]int

	// flag set to true when poll() returns due to expired access tokens
	terminated *atomic.Bool
	wg         *sync.WaitGroup

	pollHistogramVec    *prometheus.HistogramVec
	processHistogramVec *prometheus.HistogramVec
	timelineSizeVec     *prometheus.HistogramVec
}

func newPoller(pid PollerID, accessToken string, client Client, receiver V2DataReceiver, logger zerolog.Logger, initialToDeviceOnly bool) *poller {
	var wg sync.WaitGroup
	wg.Add(1)
	return &poller{
		userID:              pid.UserID,
		deviceID:            pid.DeviceID,
		accessToken:         accessToken,
		client:              client,
		receiver:            receiver,
		terminated:          &atomic.Bool{},
		logger:              logger,
		wg:                  &wg,
		initialToDeviceOnly: initialToDeviceOnly,
	}
}

// Blocks until the initial sync has been done on this poller.
func (p *poller) WaitUntilInitialSync() {
	p.wg.Wait()
}

func (p *poller) Terminate() {
	p.terminated.CompareAndSwap(false, true)
}

type pollLoopState struct {
	firstTime bool
	failCount int
	since     string
}

// Poll will block forever, repeatedly calling v2 sync. Do this in a goroutine.
// Returns if the access token gets invalidated or if there was a fatal error processing v2 responses.
// Use WaitUntilInitialSync() to wait until the first poll has been processed.
func (p *poller) Poll(since string) {
	// Basing the sentry-wrangling on the sentry-go net/http integration, see e.g.
	// https://github.com/getsentry/sentry-go/blob/02e712a638c40cd9701ad52d5d1309d65d556ef9/http/sentryhttp.go#L84
	// TODO is this the correct way to create hub? Should the cloning be done by the
	// caller and passed down?
	hub := sentry.CurrentHub().Clone()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetUser(sentry.User{Username: p.userID})
		scope.SetContext(internal.SentryCtxKey, map[string]interface{}{
			"device_id": p.deviceID,
		})
	})
	ctx := sentry.SetHubOnContext(context.Background(), hub)

	p.logger.Info().Str("since", since).Msg("Poller: v2 poll loop started")
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			logger.Error().Str("user", p.userID).Str("device", p.deviceID).Msg(string(debug.Stack()))
			internal.GetSentryHubFromContextOrDefault(ctx).RecoverWithContext(ctx, panicErr)
		}
		p.receiver.OnTerminated(ctx, p.userID, p.deviceID)
	}()

	state := pollLoopState{
		firstTime: true,
		failCount: 0,
		since:     since,
	}
	for !p.terminated.Load() {
		ctx, task := internal.StartTask(ctx, "Poll")
		err := p.poll(ctx, &state)
		task.End()
		if err != nil {
			break
		}
	}
	// always unblock EnsurePolling else we can end up head-of-line blocking other pollers!
	if state.firstTime {
		state.firstTime = false
		p.wg.Done()
	}
}

// poll is the body of the poller loop. It reads and updates a small amount of state in
// s (which is assumed to be non-nil). Returns a non-nil error iff the poller loop
// should halt.
func (p *poller) poll(ctx context.Context, s *pollLoopState) error {
	if s.failCount > 0 {
		// don't backoff when doing v2 syncs because the response is only in the cache for a short
		// period of time (on massive accounts on matrix.org) such that if you wait 2,4,8min between
		// requests it might force the server to do the work all over again :(
		waitTime := 3 * time.Second
		p.logger.Warn().Str("duration", waitTime.String()).Int("fail-count", s.failCount).Msg("Poller: waiting before next poll")
		timeSleep(waitTime)
	}
	if p.terminated.Load() {
		return fmt.Errorf("poller terminated")
	}
	start := time.Now()
	spanCtx, region := internal.StartSpan(ctx, "DoSyncV2")
	resp, statusCode, err := p.client.DoSyncV2(spanCtx, p.accessToken, s.since, s.firstTime, p.initialToDeviceOnly)
	region.End()
	p.trackRequestDuration(time.Since(start), s.since == "", s.firstTime)
	if p.terminated.Load() {
		return fmt.Errorf("poller terminated")
	}
	if err != nil {
		// check if temporary
		if statusCode != 401 {
			p.logger.Warn().Int("code", statusCode).Err(err).Msg("Poller: sync v2 poll returned temporary error")
			s.failCount += 1
			return nil
		} else {
			errMsg := "poller: access token has been invalidated, terminating loop"
			p.logger.Warn().Msg(errMsg)
			p.receiver.OnExpiredToken(ctx, hashToken(p.accessToken), p.userID, p.deviceID)
			p.Terminate()
			return fmt.Errorf(errMsg)
		}
	}
	if s.since == "" {
		p.logger.Info().Msg("Poller: valid initial sync response received")
	}
	p.initialToDeviceOnly = false
	start = time.Now()
	s.failCount = 0
	// Do the most latency-sensitive parsing first.
	// This only helps if the executor isn't already busy.
	p.parseToDeviceMessages(ctx, resp)
	p.parseE2EEData(ctx, resp)
	p.parseGlobalAccountData(ctx, resp)
	p.parseRoomsResponse(ctx, resp)

	wasInitial := s.since == ""
	wasFirst := s.firstTime

	s.since = resp.NextBatch
	// persist the since token (TODO: this could get slow if we hammer the DB too much)
	p.receiver.UpdateDeviceSince(ctx, p.userID, p.deviceID, s.since)

	if s.firstTime {
		s.firstTime = false
		p.wg.Done()
	}
	p.trackProcessDuration(time.Since(start), wasInitial, wasFirst)
	return nil
}

func (p *poller) trackRequestDuration(dur time.Duration, isInitial, isFirst bool) {
	if p.pollHistogramVec == nil {
		return
	}
	p.pollHistogramVec.WithLabelValues(labels(isInitial, isFirst)...).Observe(float64(dur.Seconds()))
}

func (p *poller) trackProcessDuration(dur time.Duration, isInitial, isFirst bool) {
	if p.processHistogramVec == nil {
		return
	}
	p.processHistogramVec.WithLabelValues(labels(isInitial, isFirst)...).Observe(float64(dur.Seconds()))
}

func labels(isInitial, isFirst bool) []string {
	l := make([]string, 2)
	if isInitial {
		l[0] = "1"
	} else {
		l[0] = "0"
	}
	if isFirst {
		l[1] = "1"
	} else {
		l[1] = "0"
	}
	return l
}

func (p *poller) parseToDeviceMessages(ctx context.Context, res *SyncResponse) {
	ctx, task := internal.StartTask(ctx, "parseToDeviceMessages")
	defer task.End()
	if len(res.ToDevice.Events) == 0 {
		return
	}
	p.receiver.AddToDeviceMessages(ctx, p.userID, p.deviceID, res.ToDevice.Events)
}

func (p *poller) parseE2EEData(ctx context.Context, res *SyncResponse) {
	ctx, task := internal.StartTask(ctx, "parseE2EEData")
	defer task.End()
	var changedOTKCounts map[string]int
	if res.DeviceListsOTKCount != nil && len(res.DeviceListsOTKCount) > 0 {
		if len(p.otkCounts) != len(res.DeviceListsOTKCount) {
			changedOTKCounts = res.DeviceListsOTKCount
		} else if p.otkCounts != nil {
			for k := range res.DeviceListsOTKCount {
				if res.DeviceListsOTKCount[k] != p.otkCounts[k] {
					changedOTKCounts = res.DeviceListsOTKCount
					break
				}
			}
		}
		p.otkCounts = res.DeviceListsOTKCount
	}
	var changedFallbackTypes []string
	if len(res.DeviceUnusedFallbackKeyTypes) > 0 {
		if len(p.fallbackKeyTypes) != len(res.DeviceUnusedFallbackKeyTypes) {
			changedFallbackTypes = res.DeviceUnusedFallbackKeyTypes
		} else {
			for i := range res.DeviceUnusedFallbackKeyTypes {
				if res.DeviceUnusedFallbackKeyTypes[i] != p.fallbackKeyTypes[i] {
					changedFallbackTypes = res.DeviceUnusedFallbackKeyTypes
					break
				}
			}
		}
		p.fallbackKeyTypes = res.DeviceUnusedFallbackKeyTypes
	}

	deviceListChanges := internal.ToDeviceListChangesMap(res.DeviceLists.Changed, res.DeviceLists.Left)

	if deviceListChanges != nil || changedFallbackTypes != nil || changedOTKCounts != nil {
		p.receiver.OnE2EEData(ctx, p.userID, p.deviceID, changedOTKCounts, changedFallbackTypes, deviceListChanges)
	}
}

func (p *poller) parseGlobalAccountData(ctx context.Context, res *SyncResponse) {
	ctx, task := internal.StartTask(ctx, "parseGlobalAccountData")
	defer task.End()
	if len(res.AccountData.Events) == 0 {
		return
	}
	p.receiver.OnAccountData(ctx, p.userID, AccountDataGlobalRoom, res.AccountData.Events)
}

func (p *poller) parseRoomsResponse(ctx context.Context, res *SyncResponse) {
	ctx, task := internal.StartTask(ctx, "parseRoomsResponse")
	defer task.End()
	stateCalls := 0
	timelineCalls := 0
	typingCalls := 0
	receiptCalls := 0
	for roomID, roomData := range res.Rooms.Join {
		if len(roomData.State.Events) > 0 {
			stateCalls++
			prependStateEvents := p.receiver.Initialise(ctx, roomID, roomData.State.Events)
			if len(prependStateEvents) > 0 {
				// The poller has just learned of these state events due to an
				// incremental poller sync; we must have missed the opportunity to see
				// these down /sync in a timeline. As a workaround, inject these into
				// the timeline now so that future events are received under the
				// correct room state.
				const warnMsg = "parseRoomsResponse: prepending state events to timeline after gappy poll"
				logger.Warn().Str("room_id", roomID).Int("prependStateEvents", len(prependStateEvents)).Msg(warnMsg)
				hub := internal.GetSentryHubFromContextOrDefault(ctx)
				hub.WithScope(func(scope *sentry.Scope) {
					scope.SetContext(internal.SentryCtxKey, map[string]interface{}{
						"room_id":                  roomID,
						"num_prepend_state_events": len(prependStateEvents),
					})
					hub.CaptureMessage(warnMsg)
				})
				roomData.Timeline.Events = append(prependStateEvents, roomData.Timeline.Events...)
			}
		}
		// process typing/receipts before events so we seed the caches correctly for when we return the room
		for _, ephEvent := range roomData.Ephemeral.Events {
			ephEventType := gjson.GetBytes(ephEvent, "type").Str
			switch ephEventType {
			case "m.typing":
				typingCalls++
				p.receiver.SetTyping(ctx, roomID, ephEvent)
			case "m.receipt":
				receiptCalls++
				p.receiver.OnReceipt(ctx, p.userID, roomID, ephEventType, ephEvent)
			}
		}

		// process account data
		if len(roomData.AccountData.Events) > 0 {
			p.receiver.OnAccountData(ctx, p.userID, roomID, roomData.AccountData.Events)
		}
		if len(roomData.Timeline.Events) > 0 {
			timelineCalls++
			p.trackTimelineSize(len(roomData.Timeline.Events), roomData.Timeline.Limited)
			p.receiver.Accumulate(ctx, p.userID, p.deviceID, roomID, roomData.Timeline.PrevBatch, roomData.Timeline.Events)
		}

		// process unread counts AFTER events so global caches have been updated by the time this metadata is added.
		// Previously we did this BEFORE events so we atomically showed the event and the unread count in one go, but
		// this could cause clients to de-sync: see TestUnreadCountMisordering integration test.
		if roomData.UnreadNotifications.HighlightCount != nil || roomData.UnreadNotifications.NotificationCount != nil {
			p.receiver.UpdateUnreadCounts(ctx, roomID, p.userID, roomData.UnreadNotifications.HighlightCount, roomData.UnreadNotifications.NotificationCount)
		}
	}
	for roomID, roomData := range res.Rooms.Leave {
		// TODO: do we care about state?
		if len(roomData.Timeline.Events) > 0 {
			p.trackTimelineSize(len(roomData.Timeline.Events), roomData.Timeline.Limited)
			p.receiver.Accumulate(ctx, p.userID, p.deviceID, roomID, roomData.Timeline.PrevBatch, roomData.Timeline.Events)
		}
		p.receiver.OnLeftRoom(ctx, p.userID, roomID)
	}
	for roomID, roomData := range res.Rooms.Invite {
		p.receiver.OnInvite(ctx, p.userID, roomID, roomData.InviteState.Events)
	}
	var l *zerolog.Event
	if len(res.Rooms.Invite) > 0 || len(res.Rooms.Join) > 0 {
		l = p.logger.Info()
	} else {
		l = p.logger.Debug()
	}
	l.Ints(
		"rooms [invite,join,leave]", []int{len(res.Rooms.Invite), len(res.Rooms.Join), len(res.Rooms.Leave)},
	).Ints(
		"storage [states,timelines,typing,receipts]", []int{stateCalls, timelineCalls, typingCalls, receiptCalls},
	).Int("to_device", len(res.ToDevice.Events)).Msg("Poller: accumulated data")
}

func (p *poller) trackTimelineSize(size int, limited bool) {
	if p.timelineSizeVec == nil {
		return
	}
	label := "unlimited"
	if limited {
		label = "limited"
	}
	p.timelineSizeVec.WithLabelValues(label).Observe(float64(size))
}
