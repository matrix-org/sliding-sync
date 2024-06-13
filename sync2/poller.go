package sync2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/exp/slices"

	"github.com/getsentry/sentry-go"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
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
	// Return an error to stop the since token advancing.
	Accumulate(ctx context.Context, userID, deviceID, roomID string, timeline TimelineResponse) error // latest pos with event nids of timeline entries
	// Initialise the room, if it hasn't been already. This means the state section of the v2 response.
	// If given a state delta from an incremental sync, returns the slice of all state events unknown to the DB.
	// Return an error to stop the since token advancing.
	Initialise(ctx context.Context, roomID string, state []json.RawMessage) error // snapshot ID?
	// SetTyping indicates which users are typing.
	SetTyping(ctx context.Context, pollerID PollerID, roomID string, ephEvent json.RawMessage)
	// Sent when there is a new receipt
	OnReceipt(ctx context.Context, userID, roomID, ephEventType string, ephEvent json.RawMessage)
	// AddToDeviceMessages adds this chunk of to_device messages. Preserve the ordering.
	// Return an error to stop the since token advancing.
	AddToDeviceMessages(ctx context.Context, userID, deviceID string, msgs []json.RawMessage) error
	// UpdateUnreadCounts sets the highlight_count and notification_count for this user in this room.
	UpdateUnreadCounts(ctx context.Context, roomID, userID string, highlightCount, notifCount *int)
	// Set the latest account data for this user.
	// Return an error to stop the since token advancing.
	OnAccountData(ctx context.Context, userID, roomID string, events []json.RawMessage) error // ping update with types? Can you race when re-querying?
	// Sent when there is a room in the `invite` section of the v2 response.
	// Return an error to stop the since token advancing.
	OnInvite(ctx context.Context, userID, roomID string, inviteState []json.RawMessage) error // invitestate in db
	// Sent when there is a room in the `leave` section of the v2 response.
	// Return an error to stop the since token advancing.
	OnLeftRoom(ctx context.Context, userID, roomID string, leaveEvent json.RawMessage) error
	// Sent when there is a _change_ in E2EE data, not all the time
	OnE2EEData(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) error
	// Sent when the poll loop terminates
	OnTerminated(ctx context.Context, pollerID PollerID)
	// Sent when the token gets a 401 response
	OnExpiredToken(ctx context.Context, accessTokenHash, userID, deviceID string)
}

type IPollerMap interface {
	EnsurePolling(pid PollerID, accessToken, v2since string, isStartup bool) (created bool, err error)
	NumPollers() int
	Terminate()
	DeviceIDs(userID string) []string
	// ExpirePollers requests that the given pollers are terminated as if their access
	// tokens had expired. Returns the number of pollers successfully terminated.
	ExpirePollers(ids []PollerID) int
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

func (h *PollerMap) ExpirePollers(pids []PollerID) int {
	h.pollerMu.Lock()
	numTerminated := 0
	var pollersToTerminate []*poller
	for _, pid := range pids {
		p, ok := h.Pollers[pid]
		if !ok || p.terminated.Load() {
			continue
		}
		pollersToTerminate = append(pollersToTerminate, p)
	}
	h.pollerMu.Unlock()
	// now terminate the pollers.
	for _, p := range pollersToTerminate {
		p.Terminate()
		// Ensure that we won't recreate this poller on startup. If it reappears later,
		// we'll make another EnsurePolling call which will recreate the poller.
		h.callbacks.OnExpiredToken(context.Background(), hashToken(p.accessToken), p.userID, p.deviceID)
		numTerminated++
	}

	return numTerminated
}

// EnsurePolling makes sure there is a poller for this device, making one if need be.
// Blocks until at least 1 sync is done if and only if the poller was just created.
// This ensures that calls to the database will return data.
// Guarantees only 1 poller will be running per deviceID.
// Note that we will immediately return if there is a poller for the same user but a different device.
// We do this to allow for logins on clients to be snappy fast, even though they won't yet have the
// to-device msgs to decrypt E2EE rooms.
func (h *PollerMap) EnsurePolling(pid PollerID, accessToken, v2since string, isStartup bool) (bool, error) {
	h.pollerMu.Lock()
	if !h.executorRunning {
		h.executorRunning = true
		go h.execute()
	}
	poller, ok := h.Pollers[pid]
	// a poller exists and hasn't been terminated so we don't need to do anything
	if ok && !poller.terminated.Load() {
		if poller.accessToken != accessToken {
			log.Warn().Msg("PollerMap.EnsurePolling: poller already running with different access token")
		}
		h.pollerMu.Unlock()
		// this existing poller may not have completed the initial sync yet, so we need to make sure
		// it has before we return.
		poller.WaitUntilInitialSync()
		return false, nil
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
	poller = newPoller(pid, accessToken, h.v2Client, h, !needToWait && !isStartup)
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
		log.Info().Str("user", poller.userID).Msg("a poller exists for this user; not waiting for this device to do an initial sync")
	}
	if poller.terminated.Load() {
		return false, fmt.Errorf("PollerMap.EnsurePolling: poller terminated after intial sync")
	}
	return true, nil
}

func (h *PollerMap) execute() {
	for fn := range h.executor {
		fn()
	}
}

func (h *PollerMap) UpdateDeviceSince(ctx context.Context, userID, deviceID, since string) {
	h.callbacks.UpdateDeviceSince(ctx, userID, deviceID, since)
}
func (h *PollerMap) Accumulate(ctx context.Context, userID, deviceID, roomID string, timeline TimelineResponse) (err error) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		err = h.callbacks.Accumulate(ctx, userID, deviceID, roomID, timeline)
		wg.Done()
	}
	wg.Wait()
	return
}
func (h *PollerMap) Initialise(ctx context.Context, roomID string, state []json.RawMessage) (err error) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		err = h.callbacks.Initialise(ctx, roomID, state)
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
func (h *PollerMap) OnInvite(ctx context.Context, userID, roomID string, inviteState []json.RawMessage) (err error) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		err = h.callbacks.OnInvite(ctx, userID, roomID, inviteState)
		wg.Done()
	}
	wg.Wait()
	return
}

func (h *PollerMap) OnLeftRoom(ctx context.Context, userID, roomID string, leaveEvent json.RawMessage) (err error) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		err = h.callbacks.OnLeftRoom(ctx, userID, roomID, leaveEvent)
		wg.Done()
	}
	wg.Wait()
	return
}

// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
// would implicitly acknowledge these messages.
func (h *PollerMap) AddToDeviceMessages(ctx context.Context, userID, deviceID string, msgs []json.RawMessage) error {
	return h.callbacks.AddToDeviceMessages(ctx, userID, deviceID, msgs)
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

func (h *PollerMap) OnAccountData(ctx context.Context, userID, roomID string, events []json.RawMessage) (err error) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		err = h.callbacks.OnAccountData(ctx, userID, roomID, events)
		wg.Done()
	}
	wg.Wait()
	return
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

func (h *PollerMap) OnE2EEData(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) error {
	// This is device-scoped data and will never race with another poller. Therefore we
	// do not need to queue this up in the executor. However: the poller does need to
	// wait for this to complete before advancing the since token, or else we risk
	// losing device list changes.
	return h.callbacks.OnE2EEData(ctx, userID, deviceID, otkCounts, fallbackKeyTypes, deviceListChanges)
}

// Poller can automatically poll the sync v2 endpoint and accumulate the responses in storage
type poller struct {
	userID      string
	deviceID    string
	accessToken string
	client      Client
	receiver    V2DataReceiver

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

func newPoller(pid PollerID, accessToken string, client Client, receiver V2DataReceiver, initialToDeviceOnly bool) *poller {
	var wg sync.WaitGroup
	wg.Add(1)
	return &poller{
		userID:              pid.UserID,
		deviceID:            pid.DeviceID,
		accessToken:         accessToken,
		client:              client,
		receiver:            receiver,
		terminated:          &atomic.Bool{},
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
		scope.SetUser(sentry.User{Username: p.userID, ID: p.deviceID})
	})
	ctx := sentry.SetHubOnContext(context.Background(), hub)

	log.Info().Str("since", since).Msg("Poller: v2 poll loop started")
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			log.Error().Str("user", p.userID).Str("device", p.deviceID).Msgf("%s. Traceback:\n%s", panicErr, debug.Stack())
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
		if s.failCount > 1000 {
			// 3s * 1000 = 3000s = 50 minutes
			errMsg := "poller: access token has failed >1000 times to /sync, terminating loop"
			log.Warn().Msg(errMsg)
			p.receiver.OnExpiredToken(ctx, hashToken(p.accessToken), p.userID, p.deviceID)
			p.Terminate()
			return fmt.Errorf(errMsg)
		}
		// don't backoff when doing v2 syncs because the response is only in the cache for a short
		// period of time (on massive accounts on matrix.org) such that if you wait 2,4,8min between
		// requests it might force the server to do the work all over again :(
		waitTime := 3 * time.Second
		log.Warn().Str("duration", waitTime.String()).Int("fail-count", s.failCount).Msg("Poller: waiting before next poll")
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
			log.Warn().Int("code", statusCode).Err(err).Msg("Poller: sync v2 poll returned temporary error")
			s.failCount += 1
			return nil
		} else {
			errMsg := "poller: access token has been invalidated, terminating loop"
			log.Warn().Msg(errMsg)
			p.receiver.OnExpiredToken(ctx, hashToken(p.accessToken), p.userID, p.deviceID)
			p.Terminate()
			return fmt.Errorf(errMsg)
		}
	}
	if s.since == "" {
		log.Info().Msg("Poller: valid initial sync response received")
	}
	p.initialToDeviceOnly = false
	start = time.Now()
	s.failCount = 0

	// If any of these sections return an error, we will NOT increment the since token and so
	// retry processing the same response after a brief period
	retryErr := p.parseE2EEData(ctx, resp)
	if shouldRetry(retryErr) {
		log.Err(retryErr).Msg("Poller: parseE2EEData returned an error")
		s.failCount += 1
		return nil
	}
	retryErr = p.parseGlobalAccountData(ctx, resp)
	if shouldRetry(retryErr) {
		log.Err(retryErr).Msg("Poller: parseGlobalAccountData returned an error")
		s.failCount += 1
		return nil
	}
	retryErr = p.parseRoomsResponse(ctx, resp)
	if shouldRetry(retryErr) {
		log.Err(retryErr).Msg("Poller: parseRoomsResponse returned an error")
		s.failCount += 1
		return nil
	}
	// process to-device messages as the LAST retryable data so we don't double-process
	// to-device msgs on retrys. In other words, if parseToDeviceMessages returns no error
	// then we for sure are going to increment the since token, so cannot see duplicates.
	// If parseToDeviceMessages was earlier, a later parse function could force a retry,
	// causing duplicates. Other parse functions don't have this problem as they are
	// deduplicated.
	retryErr = p.parseToDeviceMessages(ctx, resp)
	if shouldRetry(retryErr) {
		log.Err(retryErr).Msg("Poller: parseToDeviceMessages returned an error")
		s.failCount += 1
		return nil
	}

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

func shouldRetry(retryErr error) bool {
	if retryErr == nil {
		return false
	}
	// we retry on all errors EXCEPT DataError as this indicates that retrying won't help
	var de *internal.DataError
	return !errors.As(retryErr, &de)
}

func (p *poller) parseToDeviceMessages(ctx context.Context, res *SyncResponse) error {
	ctx, task := internal.StartTask(ctx, "parseToDeviceMessages")
	defer task.End()
	if len(res.ToDevice.Events) == 0 {
		return nil
	}
	p.totalDeviceEvents += len(res.ToDevice.Events)
	return p.receiver.AddToDeviceMessages(ctx, p.userID, p.deviceID, res.ToDevice.Events)
}

func (p *poller) parseE2EEData(ctx context.Context, res *SyncResponse) error {
	ctx, task := internal.StartTask(ctx, "parseE2EEData")
	defer task.End()
	var changedOTKCounts map[string]int
	shouldSetOTKs := false
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
		shouldSetOTKs = true
	}
	var changedFallbackTypes []string // nil slice == don't set, empty slice = no fallback key
	shouldSetFallbackKeys := false
	if len(p.fallbackKeyTypes) != len(res.DeviceUnusedFallbackKeyTypes) {
		// length mismatch always causes an update
		changedFallbackTypes = res.DeviceUnusedFallbackKeyTypes
		shouldSetFallbackKeys = true
	} else {
		// lengths match, if they are non-zero then compare each element.
		// if they are zero, check for nil vs empty slice.
		if len(res.DeviceUnusedFallbackKeyTypes) == 0 {
			isCurrentNil := res.DeviceUnusedFallbackKeyTypes == nil
			isPreviousNil := p.fallbackKeyTypes == nil
			if isCurrentNil != isPreviousNil {
				shouldSetFallbackKeys = true
				changedFallbackTypes = []string{}
			}
		}
		for i := range res.DeviceUnusedFallbackKeyTypes {
			if res.DeviceUnusedFallbackKeyTypes[i] != p.fallbackKeyTypes[i] {
				changedFallbackTypes = res.DeviceUnusedFallbackKeyTypes
				shouldSetFallbackKeys = true
				break
			}
		}
	}

	deviceListChanges := internal.ToDeviceListChangesMap(res.DeviceLists.Changed, res.DeviceLists.Left)

	if deviceListChanges != nil || changedFallbackTypes != nil || changedOTKCounts != nil {
		p.totalChangedDeviceLists += len(res.DeviceLists.Changed)
		p.totalLeftDeviceLists += len(res.DeviceLists.Left)
		err := p.receiver.OnE2EEData(ctx, p.userID, p.deviceID, changedOTKCounts, changedFallbackTypes, deviceListChanges)
		if err != nil {
			return err
		}
	}
	// we should only update our internal state if OnE2EEData returns no error. Otherwise, we would fail to re-process the
	// retried response as there would be no diff between internal state and the response received.
	if shouldSetOTKs {
		p.otkCounts = res.DeviceListsOTKCount
	}
	if shouldSetFallbackKeys {
		p.fallbackKeyTypes = res.DeviceUnusedFallbackKeyTypes
	}
	return nil
}

func (p *poller) parseGlobalAccountData(ctx context.Context, res *SyncResponse) error {
	ctx, task := internal.StartTask(ctx, "parseGlobalAccountData")
	defer task.End()
	if len(res.AccountData.Events) == 0 {
		return nil
	}
	p.totalAccountData += len(res.AccountData.Events)
	return p.receiver.OnAccountData(ctx, p.userID, AccountDataGlobalRoom, res.AccountData.Events)
}

func (p *poller) parseRoomsResponse(ctx context.Context, res *SyncResponse) error {
	ctx, task := internal.StartTask(ctx, "parseRoomsResponse")
	defer task.End()
	stateCalls := 0
	timelineCalls := 0
	typingCalls := 0
	receiptCalls := 0
	// try to process all rooms, rather than bailing out at the first room which returns an error.
	// This is CRITICAL if the error returned is an `internal.DataError` as in that case we will
	// NOT RETRY THE SYNC REQUEST, meaning if we didn't process all the rooms we would lose data.
	// Currently, Accumulate/Initialise can return DataErrors when a new room is seen without a
	// create event.
	// NOTE: we process rooms non-deterministically (ranging over keys in a map).
	var lastErrs []error
	for roomID, roomData := range res.Rooms.Join {
		if len(roomData.State.Events) > 0 {
			stateCalls++
			if roomData.Timeline.Limited {
				p.trackGappyStateSize(len(roomData.State.Events))
			}
			err := p.receiver.Initialise(ctx, roomID, roomData.State.Events)
			if err != nil {
				_, ok := err.(*internal.DataError)
				if ok {
					// This typically happens when we are missing an m.room.create event.
					// Synapse may sometimes send the m.room.create event erroneously in the timeline,
					// so check if that is the case here. See https://github.com/matrix-org/complement/pull/690
					var createEvent json.RawMessage
					for i, ev := range roomData.Timeline.Events {
						evv := gjson.ParseBytes(ev)
						if evv.Get("type").Str == "m.room.create" && evv.Get("state_key").Exists() && evv.Get("state_key").Str == "" {
							createEvent = roomData.Timeline.Events[i]
							// remove the create event from the timeline so we don't double process it
							roomData.Timeline.Events = append(roomData.Timeline.Events[:i], roomData.Timeline.Events[i+1:]...)
							break
						}
					}
					if createEvent != nil {
						roomData.State.Events = slices.Insert(roomData.State.Events, 0, createEvent)
						// retry the processing of the room state
						err = p.receiver.Initialise(ctx, roomID, roomData.State.Events)
						if err == nil {
							const warnMsg = "parseRoomsResponse: m.room.create event was found in the timeline not state, info after moving create event"
							log.Warn().Str("user_id", p.userID).Str("room_id", roomID).Int(
								"timeline", len(roomData.Timeline.Events),
							).Int("state", len(roomData.State.Events)).Msg(warnMsg)
							hub := internal.GetSentryHubFromContextOrDefault(ctx)
							hub.WithScope(func(scope *sentry.Scope) {
								scope.SetContext(internal.SentryCtxKey, map[string]interface{}{
									"room_id":  roomID,
									"timeline": len(roomData.Timeline.Events),
									"state":    len(roomData.State.Events),
								})
								hub.CaptureMessage(warnMsg)
							})
						}
					}
				}
				// either err isn't a data error OR we retried Initialise and it still returned an error
				// either way, give up.
				if err != nil {
					lastErrs = append(lastErrs, fmt.Errorf("Initialise[%s]: %w", roomID, err))
					continue
				}
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
			err := p.receiver.OnAccountData(ctx, p.userID, roomID, roomData.AccountData.Events)
			if err != nil {
				lastErrs = append(lastErrs, fmt.Errorf("OnAccountData[%s]: %w", roomID, err))
				continue
			}
		}
		if len(roomData.Timeline.Events) > 0 {
			timelineCalls++
			p.trackTimelineSize(len(roomData.Timeline.Events), roomData.Timeline.Limited)

			err := p.receiver.Accumulate(ctx, p.userID, p.deviceID, roomID, roomData.Timeline)
			if err != nil {
				lastErrs = append(lastErrs, fmt.Errorf("Accumulate[%s]: %w", roomID, err))
				continue
			}
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
			err := p.receiver.Accumulate(ctx, p.userID, p.deviceID, roomID, roomData.Timeline)
			if err != nil {
				lastErrs = append(lastErrs, fmt.Errorf("Accumulate_Leave[%s]: %w", roomID, err))
				continue
			}
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
			err := p.receiver.OnLeftRoom(ctx, p.userID, roomID, leaveEvent)
			if err != nil {
				lastErrs = append(lastErrs, fmt.Errorf("OnLeftRoom[%s]: %w", roomID, err))
				continue
			}
		}
	}
	for roomID, roomData := range res.Rooms.Invite {
		err := p.receiver.OnInvite(ctx, p.userID, roomID, roomData.InviteState.Events)
		if err != nil {
			lastErrs = append(lastErrs, fmt.Errorf("OnInvite[%s]: %w", roomID, err))
		}
	}

	p.totalReceipts += receiptCalls
	p.totalStateCalls += stateCalls
	p.totalTimelineCalls += timelineCalls
	p.totalTyping += typingCalls
	p.totalInvites += len(res.Rooms.Invite)
	if len(lastErrs) == 0 {
		return nil
	}
	if len(lastErrs) == 1 {
		return lastErrs[0]
	}
	// there are 2 classes of error:
	// - non-retriable errors (aka internal.DataError)
	// - retriable errors (transient DB connection failures, etc)
	// If we have ANY retriable error they need to take priority over the non-retriable error else we will lose data.
	// E.g in the case where a single sync response has room A with bad data (so wants to skip over it) and room B
	// with good data but the DB got pulled momentarily, we want to retry and NOT advance the since token else we will
	// lose the events in room B.
	// To implement this, we _strip out_ any data errors and return that. If they are all data errors then we just return them.
	var retriableOnlyErrs []error
	for _, e := range lastErrs {
		e := e
		if shouldRetry(e) {
			retriableOnlyErrs = append(retriableOnlyErrs, e)
		}
	}
	// return retriable errors as a priority over unretriable
	if len(retriableOnlyErrs) > 0 {
		return errors.Join(retriableOnlyErrs...)
	}
	// we only have unretriable errors, so return them
	return errors.Join(lastErrs...)
}

func (p *poller) maybeLogStats(force bool) {
	if !force && timeSince(p.lastLogged) < logInterval {
		// only log at most once every logInterval
		return
	}
	p.lastLogged = time.Now()
	log.Info().Ints(
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
