package sync2

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

type PollerID struct {
	UserID   string
	DeviceID string
}

// alias time.Sleep/time.Since so tests can monkey patch it out
var timeSleep = time.Sleep
var timeSince = time.Since

// log at most once every duration. Always logs before terminating.
var logInterval = 30 * time.Second

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
	SetTyping(ctx context.Context, pollerID PollerID, roomID string, ephEvent json.RawMessage)
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
	OnLeftRoom(ctx context.Context, userID, roomID string, leaveEvent json.RawMessage)
	// Sent when there is a _change_ in E2EE data, not all the time
	OnE2EEData(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int)
	// Sent when the poll loop terminates
	OnTerminated(ctx context.Context, pollerID PollerID)
	// Sent when the token gets a 401 response
	OnExpiredToken(ctx context.Context, accessTokenHash, userID, deviceID string)
}

type IPollerMap interface {
	EnsurePolling(pid PollerID, accessToken, v2since string, isStartup bool, logger zerolog.Logger) (created bool)
	NumPollers() int
	Terminate()
	DeviceIDs(userID string) []string
}

// PollerMap is a map of device ID to Poller
type PollerMap struct {
	v2Client                    Client
	callbacks                   V2DataReceiver
	pollerMu                    *sync.Mutex
	Pollers                     map[PollerID]*poller
	executor                    chan func()
	executorRunning             bool
	processHistogramVec         *prometheus.HistogramVec
	timelineSizeHistogramVec    *prometheus.HistogramVec
	gappyStateSizeVec           *prometheus.HistogramVec
	numOutstandingSyncReqsGauge prometheus.Gauge
	totalNumPollsCounter        prometheus.Counter
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
		pm.gappyStateSizeVec = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "sliding_sync",
			Subsystem: "poller",
			Name:      "gappy_state_size",
			Help:      "Number of events in a state block during a sync v2 gappy sync",
			Buckets:   []float64{1.0, 10.0, 100.0, 1000.0, 10000.0},
		}, nil)
		prometheus.MustRegister(pm.gappyStateSizeVec)
		pm.totalNumPollsCounter = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sliding_sync",
			Subsystem: "poller",
			Name:      "total_num_polls",
			Help:      "Total number of poll loops iterated.",
		})
		prometheus.MustRegister(pm.totalNumPollsCounter)
		pm.numOutstandingSyncReqsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sliding_sync",
			Subsystem: "poller",
			Name:      "num_outstanding_sync_v2_reqs",
			Help:      "Number of sync v2 requests that have yet to return a response.",
		})
		prometheus.MustRegister(pm.numOutstandingSyncReqsGauge)
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
	if h.processHistogramVec != nil {
		prometheus.Unregister(h.processHistogramVec)
	}
	if h.timelineSizeHistogramVec != nil {
		prometheus.Unregister(h.timelineSizeHistogramVec)
	}
	if h.gappyStateSizeVec != nil {
		prometheus.Unregister(h.gappyStateSizeVec)
	}
	if h.totalNumPollsCounter != nil {
		prometheus.Unregister(h.totalNumPollsCounter)
	}
	if h.numOutstandingSyncReqsGauge != nil {
		prometheus.Unregister(h.numOutstandingSyncReqsGauge)
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

// DeviceIDs returns the slice of all devices currently being polled for by this user.
// The return value is brand-new and is fully owned by the caller.
func (h *PollerMap) DeviceIDs(userID string) []string {
	h.pollerMu.Lock()
	defer h.pollerMu.Unlock()
	var devices []string
	for _, p := range h.Pollers {
		if !p.terminated.Load() && p.userID == userID {
			devices = append(devices, p.deviceID)
		}
	}
	return devices
}

// EnsurePolling makes sure there is a poller for this device, making one if need be.
// Blocks until at least 1 sync is done if and only if the poller was just created.
// This ensures that calls to the database will return data.
// Guarantees only 1 poller will be running per deviceID.
// Note that we will immediately return if there is a poller for the same user but a different device.
// We do this to allow for logins on clients to be snappy fast, even though they won't yet have the
// to-device msgs to decrypt E2EE rooms.
func (h *PollerMap) EnsurePolling(pid PollerID, accessToken, v2since string, isStartup bool, logger zerolog.Logger) bool {
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
		return false
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
	poller.processHistogramVec = h.processHistogramVec
	poller.timelineSizeVec = h.timelineSizeHistogramVec
	poller.gappyStateSizeVec = h.gappyStateSizeVec
	poller.numOutstandingSyncReqs = h.numOutstandingSyncReqsGauge
	poller.totalNumPolls = h.totalNumPollsCounter
	go poller.Poll(v2since)
	h.Pollers[pid] = poller

	h.pollerMu.Unlock()
	if needToWait {
		poller.WaitUntilInitialSync()
	} else {
		logger.Info().Str("user", poller.userID).Msg("a poller exists for this user; not waiting for this device to do an initial sync")
	}
	return true
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
func (h *PollerMap) SetTyping(ctx context.Context, pollerID PollerID, roomID string, ephEvent json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.SetTyping(ctx, pollerID, roomID, ephEvent)
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

func (h *PollerMap) OnLeftRoom(ctx context.Context, userID, roomID string, leaveEvent json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnLeftRoom(ctx, userID, roomID, leaveEvent)
		wg.Done()
	}
	wg.Wait()
}

// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
// would implicitly acknowledge these messages.
func (h *PollerMap) AddToDeviceMessages(ctx context.Context, userID, deviceID string, msgs []json.RawMessage) {
	h.callbacks.AddToDeviceMessages(ctx, userID, deviceID, msgs)
}

func (h *PollerMap) OnTerminated(ctx context.Context, pollerID PollerID) {
	h.callbacks.OnTerminated(ctx, pollerID)
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

	// stats about poll response data, for logging purposes
	lastLogged              time.Time
	totalStateCalls         int
	totalTimelineCalls      int
	totalReceipts           int
	totalTyping             int
	totalInvites            int
	totalDeviceEvents       int
	totalAccountData        int
	totalChangedDeviceLists int
	totalLeftDeviceLists    int

	pollHistogramVec       *prometheus.HistogramVec
	processHistogramVec    *prometheus.HistogramVec
	timelineSizeVec        *prometheus.HistogramVec
	gappyStateSizeVec      *prometheus.HistogramVec
	numOutstandingSyncReqs prometheus.Gauge
	totalNumPolls          prometheus.Counter
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
	firstTime       bool
	failCount       int
	since           string
	lastStoredSince time.Time // The time we last stored the since token in the database
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
			logger.Error().Str("user", p.userID).Str("device", p.deviceID).Msgf("%s. Traceback:\n%s", panicErr, debug.Stack())
			internal.GetSentryHubFromContextOrDefault(ctx).RecoverWithContext(ctx, panicErr)
		}
		p.receiver.OnTerminated(ctx, PollerID{
			UserID:   p.userID,
			DeviceID: p.deviceID,
		})
	}()

	state := pollLoopState{
		firstTime: true,
		failCount: 0,
		since:     since,
		// Setting time.Time{} results in the first poll loop to immediately store the since token.
		lastStoredSince: time.Time{},
	}
	for !p.terminated.Load() {
		ctx, task := internal.StartTask(ctx, "Poll")
		err := p.poll(ctx, &state)
		task.End()
		if err != nil {
			break
		}
	}
	p.maybeLogStats(true)
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
	if p.totalNumPolls != nil {
		p.totalNumPolls.Inc()
	}
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
	if p.numOutstandingSyncReqs != nil {
		p.numOutstandingSyncReqs.Inc()
	}
	resp, statusCode, err := p.client.DoSyncV2(spanCtx, p.accessToken, s.since, s.firstTime, p.initialToDeviceOnly)
	if p.numOutstandingSyncReqs != nil {
		p.numOutstandingSyncReqs.Dec()
	}
	region.End()
	p.trackRequestDuration(timeSince(start), s.since == "", s.firstTime)
	if p.terminated.Load() {
		return fmt.Errorf("poller terminated")
	}
	if err != nil {
		// check if temporary
		isFatal := statusCode == 401 || statusCode == 403
		if !isFatal {
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
	// Persist the since token if it either was more than one minute ago since we
	// last stored it OR the response contains to-device messages
	if timeSince(s.lastStoredSince) > time.Minute || len(resp.ToDevice.Events) > 0 {
		p.receiver.UpdateDeviceSince(ctx, p.userID, p.deviceID, s.since)
		s.lastStoredSince = time.Now()
	}

	if s.firstTime {
		s.firstTime = false
		p.wg.Done()
	}
	p.trackProcessDuration(timeSince(start), wasInitial, wasFirst)
	p.maybeLogStats(false)
	return nil
}

func (p *poller) trackRequestDuration(dur time.Duration, isInitial, isFirst bool) {
	if p.pollHistogramVec == nil {
		return
	}
	p.pollHistogramVec.WithLabelValues(labels(isInitial, isFirst)...).Observe(float64(dur.Milliseconds()))
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
	p.totalDeviceEvents += len(res.ToDevice.Events)
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
		p.totalChangedDeviceLists += len(res.DeviceLists.Changed)
		p.totalLeftDeviceLists += len(res.DeviceLists.Left)
		p.receiver.OnE2EEData(ctx, p.userID, p.deviceID, changedOTKCounts, changedFallbackTypes, deviceListChanges)
	}
}

func (p *poller) parseGlobalAccountData(ctx context.Context, res *SyncResponse) {
	ctx, task := internal.StartTask(ctx, "parseGlobalAccountData")
	defer task.End()
	if len(res.AccountData.Events) == 0 {
		return
	}
	p.totalAccountData += len(res.AccountData.Events)
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
				p.trackGappyStateSize(len(prependStateEvents))
				roomData.Timeline.Events = append(prependStateEvents, roomData.Timeline.Events...)
			}
		}
		// process typing/receipts before events so we seed the caches correctly for when we return the room
		for _, ephEvent := range roomData.Ephemeral.Events {
			ephEventType := gjson.GetBytes(ephEvent, "type").Str
			switch ephEventType {
			case "m.typing":
				typingCalls++
				p.receiver.SetTyping(ctx, PollerID{UserID: p.userID, DeviceID: p.deviceID}, roomID, ephEvent)
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
		if len(roomData.Timeline.Events) > 0 {
			p.trackTimelineSize(len(roomData.Timeline.Events), roomData.Timeline.Limited)
			p.receiver.Accumulate(ctx, p.userID, p.deviceID, roomID, roomData.Timeline.PrevBatch, roomData.Timeline.Events)
		}
		// Pass the leave event directly to OnLeftRoom. We need to do this _in addition_ to calling Accumulate to handle
		// the case where a user rejects an invite (there will be no room state, but the user still expects to see the leave event).
		var leaveEvent json.RawMessage
		for _, ev := range roomData.Timeline.Events {
			leaveEv := gjson.ParseBytes(ev)
			if leaveEv.Get("content.membership").Str == "leave" && leaveEv.Get("state_key").Str == p.userID {
				leaveEvent = ev
				break
			}
		}
		if leaveEvent != nil {
			p.receiver.OnLeftRoom(ctx, p.userID, roomID, leaveEvent)
		}
	}
	for roomID, roomData := range res.Rooms.Invite {
		p.receiver.OnInvite(ctx, p.userID, roomID, roomData.InviteState.Events)
	}

	p.totalReceipts += receiptCalls
	p.totalStateCalls += stateCalls
	p.totalTimelineCalls += timelineCalls
	p.totalTyping += typingCalls
	p.totalInvites += len(res.Rooms.Invite)
}

func (p *poller) maybeLogStats(force bool) {
	if !force && timeSince(p.lastLogged) < logInterval {
		// only log at most once every logInterval
		return
	}
	p.lastLogged = time.Now()
	p.logger.Info().Ints(
		"rooms [timeline,state,typing,receipts,invites]", []int{
			p.totalTimelineCalls, p.totalStateCalls, p.totalTyping, p.totalReceipts, p.totalInvites,
		},
	).Ints(
		"device [events,changed,left,account]", []int{
			p.totalDeviceEvents, p.totalChangedDeviceLists, p.totalLeftDeviceLists, p.totalAccountData,
		},
	).Msg("Poller: accumulated data")

	p.totalAccountData = 0
	p.totalChangedDeviceLists = 0
	p.totalDeviceEvents = 0
	p.totalInvites = 0
	p.totalLeftDeviceLists = 0
	p.totalReceipts = 0
	p.totalStateCalls = 0
	p.totalTimelineCalls = 0
	p.totalTyping = 0
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

func (p *poller) trackGappyStateSize(size int) {
	if p.gappyStateSizeVec == nil {
		return
	}
	p.gappyStateSizeVec.WithLabelValues().Observe(float64(size))
}
