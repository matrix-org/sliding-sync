package sync2

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

// alias time.Sleep so tests can monkey patch it out
var timeSleep = time.Sleep

// V2DataReceiver is the receiver for all the v2 sync data the poller gets
type V2DataReceiver interface {
	// Update the since token for this device. Called AFTER all other data in this sync response has been processed.
	UpdateDeviceSince(deviceID, since string)
	// Accumulate data for this room. This means the timeline section of the v2 response.
	Accumulate(userID, roomID, prevBatch string, timeline []json.RawMessage) // latest pos with event nids of timeline entries
	// Initialise the room, if it hasn't been already. This means the state section of the v2 response.
	Initialise(roomID string, state []json.RawMessage) // snapshot ID?
	// SetTyping indicates which users are typing.
	SetTyping(roomID string, ephEvent json.RawMessage)
	// Sent when there is a new receipt
	OnReceipt(userID, roomID, ephEventType string, ephEvent json.RawMessage)
	// AddToDeviceMessages adds this chunk of to_device messages. Preserve the ordering.
	AddToDeviceMessages(userID, deviceID string, msgs []json.RawMessage) // start/end stream pos
	// UpdateUnreadCounts sets the highlight_count and notification_count for this user in this room.
	UpdateUnreadCounts(roomID, userID string, highlightCount, notifCount *int)
	// Set the latest account data for this user.
	OnAccountData(userID, roomID string, events []json.RawMessage) // ping update with types? Can you race when re-querying?
	// Sent when there is a room in the `invite` section of the v2 response.
	OnInvite(userID, roomID string, inviteState []json.RawMessage) // invitestate in db
	// Sent when there is a room in the `leave` section of the v2 response.
	OnLeftRoom(userID, roomID string)
	// Sent when there is a _change_ in E2EE data, not all the time
	OnE2EEData(userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int)
	// Sent when the upstream homeserver sends back a 401 invalidating the token
	OnTerminated(userID, deviceID string)
}

// Fetcher used by the E2EE extension
type E2EEFetcher interface {
	DeviceData(userID, deviceID string, isInitial bool) *internal.DeviceData
}

type TransactionIDFetcher interface {
	TransactionIDForEvents(userID string, eventIDs []string) (eventIDToTxnID map[string]string)
}

// PollerMap is a map of device ID to Poller
type PollerMap struct {
	v2Client            Client
	callbacks           V2DataReceiver
	pollerMu            *sync.Mutex
	Pollers             map[string]*poller // device_id -> poller
	executor            chan func()
	executorRunning     bool
	processHistogramVec *prometheus.HistogramVec
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
		Pollers:  make(map[string]*poller),
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
	close(h.executor)
}

func (h *PollerMap) NumPollers() (count int) {
	h.pollerMu.Lock()
	defer h.pollerMu.Unlock()
	for _, p := range h.Pollers {
		if !p.Terminated {
			count++
		}
	}
	return
}

// EnsurePolling makes sure there is a poller for this user, making one if need be.
// Blocks until at least 1 sync is done if and only if the poller was just created.
// This ensures that calls to the database will return data.
// Guarantees only 1 poller will be running per deviceID.
// Note that we will immediately return if there is a poller for the same user but a different device.
// We do this to allow for logins on clients to be snappy fast, even though they won't yet have the
// to-device msgs to decrypt E2EE roms.
func (h *PollerMap) EnsurePolling(accessToken, userID, deviceID, v2since string, logger zerolog.Logger) {
	h.pollerMu.Lock()
	logger.Info().Str("device", deviceID).Msg("EnsurePolling lock acquired")
	if !h.executorRunning {
		h.executorRunning = true
		go h.execute()
	}
	poller, ok := h.Pollers[deviceID]
	// a poller exists and hasn't been terminated so we don't need to do anything
	if ok && !poller.Terminated {
		h.pollerMu.Unlock()
		// this existing poller may not have completed the initial sync yet, so we need to make sure
		// it has before we return.
		poller.WaitUntilInitialSync()
		return
	}
	// replace the poller
	poller = newPoller(userID, accessToken, deviceID, h.v2Client, h, logger)
	poller.processHistogramVec = h.processHistogramVec
	go poller.Poll(v2since)
	h.Pollers[deviceID] = poller

	// check if we need to wait at all: we don't need to if this user is already syncing on a different device
	// This is O(n) so we may want to map this if we get a lot of users...
	needToWait := true
	for pollerDeviceID, poller := range h.Pollers {
		if deviceID == pollerDeviceID {
			continue
		}
		if poller.userID == userID && !poller.Terminated {
			needToWait = false
		}
	}

	h.pollerMu.Unlock()
	if needToWait {
		poller.WaitUntilInitialSync()
	} else {
		logger.Info().Str("user", userID).Msg("a poller exists for this user; not waiting for this device to do an initial sync")
	}
}

func (h *PollerMap) execute() {
	for fn := range h.executor {
		fn()
	}
}

func (h *PollerMap) UpdateDeviceSince(deviceID, since string) {
	h.callbacks.UpdateDeviceSince(deviceID, since)
}
func (h *PollerMap) Accumulate(userID, roomID, prevBatch string, timeline []json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.Accumulate(userID, roomID, prevBatch, timeline)
		wg.Done()
	}
	wg.Wait()
}
func (h *PollerMap) Initialise(roomID string, state []json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.Initialise(roomID, state)
		wg.Done()
	}
	wg.Wait()
}
func (h *PollerMap) SetTyping(roomID string, ephEvent json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.SetTyping(roomID, ephEvent)
		wg.Done()
	}
	wg.Wait()
}
func (h *PollerMap) OnInvite(userID, roomID string, inviteState []json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnInvite(userID, roomID, inviteState)
		wg.Done()
	}
	wg.Wait()
}

func (h *PollerMap) OnLeftRoom(userID, roomID string) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnLeftRoom(userID, roomID)
		wg.Done()
	}
	wg.Wait()
}

// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
// would implicitly acknowledge these messages.
func (h *PollerMap) AddToDeviceMessages(userID, deviceID string, msgs []json.RawMessage) {
	h.callbacks.AddToDeviceMessages(userID, deviceID, msgs)
}

func (h *PollerMap) OnTerminated(userID, deviceID string) {
	h.callbacks.OnTerminated(userID, deviceID)
}

func (h *PollerMap) UpdateUnreadCounts(roomID, userID string, highlightCount, notifCount *int) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.UpdateUnreadCounts(roomID, userID, highlightCount, notifCount)
		wg.Done()
	}
	wg.Wait()
}

func (h *PollerMap) OnAccountData(userID, roomID string, events []json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnAccountData(userID, roomID, events)
		wg.Done()
	}
	wg.Wait()
}

func (h *PollerMap) OnReceipt(userID, roomID, ephEventType string, ephEvent json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnReceipt(userID, roomID, ephEventType, ephEvent)
		wg.Done()
	}
	wg.Wait()
}

func (h *PollerMap) OnE2EEData(userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnE2EEData(userID, deviceID, otkCounts, fallbackKeyTypes, deviceListChanges)
		wg.Done()
	}
	wg.Wait()
}

// Poller can automatically poll the sync v2 endpoint and accumulate the responses in storage
type poller struct {
	userID      string
	accessToken string
	deviceID    string
	client      Client
	receiver    V2DataReceiver
	logger      zerolog.Logger

	// E2EE fields: we keep them so we only send callbacks on deltas not all the time
	fallbackKeyTypes []string
	otkCounts        map[string]int

	// flag set to true when poll() returns due to expired access tokens
	Terminated  bool
	terminateCh chan struct{}
	wg          *sync.WaitGroup

	pollHistogramVec    *prometheus.HistogramVec
	processHistogramVec *prometheus.HistogramVec
}

func newPoller(userID, accessToken, deviceID string, client Client, receiver V2DataReceiver, logger zerolog.Logger) *poller {
	var wg sync.WaitGroup
	wg.Add(1)
	return &poller{
		accessToken: accessToken,
		userID:      userID,
		deviceID:    deviceID,
		client:      client,
		receiver:    receiver,
		Terminated:  false,
		terminateCh: make(chan struct{}),
		logger:      logger,
		wg:          &wg,
	}
}

// Blocks until the initial sync has been done on this poller.
func (p *poller) WaitUntilInitialSync() {
	p.wg.Wait()
}

func (p *poller) Terminate() {
	if p.Terminated {
		return
	}
	p.Terminated = true
	close(p.terminateCh)
}

func (p *poller) isTerminated() bool {
	select {
	case <-p.terminateCh:
		p.Terminated = true
		return true
	default:
		// not yet terminated
	}
	return false
}

// Poll will block forever, repeatedly calling v2 sync. Do this in a goroutine.
// Returns if the access token gets invalidated or if there was a fatal error processing v2 responses.
// Use WaitUntilInitialSync() to wait until the first poll has been processed.
func (p *poller) Poll(since string) {
	p.logger.Info().Str("since", since).Msg("Poller: v2 poll loop started")
	defer func() {
		p.receiver.OnTerminated(p.userID, p.deviceID)
	}()
	failCount := 0
	firstTime := true
	for !p.Terminated {
		if failCount > 0 {
			// don't backoff when doing v2 syncs because the response is only in the cache for a short
			// period of time (on massive accounts on matrix.org) such that if you wait 2,4,8min between
			// requests it might force the server to do the work all over again :(
			waitTime := 3 * time.Second
			p.logger.Warn().Str("duration", waitTime.String()).Int("fail-count", failCount).Msg("Poller: waiting before next poll")
			timeSleep(waitTime)
		}
		if p.isTerminated() {
			break
		}
		start := time.Now()
		resp, statusCode, err := p.client.DoSyncV2(context.Background(), p.accessToken, since, firstTime)
		p.trackRequestDuration(time.Since(start), since == "", firstTime)
		if p.isTerminated() {
			break
		}
		if err != nil {
			// check if temporary
			if statusCode != 401 {
				p.logger.Warn().Int("code", statusCode).Err(err).Msg("Poller: sync v2 poll returned temporary error")
				failCount += 1
				continue
			} else {
				p.logger.Warn().Msg("Poller: access token has been invalidated, terminating loop")
				p.Terminated = true
				break
			}
		}
		if since == "" {
			p.logger.Info().Msg("Poller: valid initial sync response received")
		}
		start = time.Now()
		failCount = 0
		p.parseE2EEData(resp)
		p.parseGlobalAccountData(resp)
		p.parseRoomsResponse(resp)
		p.parseToDeviceMessages(resp)

		wasInitial := since == ""
		wasFirst := firstTime

		since = resp.NextBatch
		// persist the since token (TODO: this could get slow if we hammer the DB too much)
		p.receiver.UpdateDeviceSince(p.deviceID, since)

		if firstTime {
			firstTime = false
			p.wg.Done()
		}
		p.trackProcessDuration(time.Since(start), wasInitial, wasFirst)
	}
	// always unblock EnsurePolling else we can end up head-of-line blocking other pollers!
	if firstTime {
		firstTime = false
		p.wg.Done()
	}
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

func (p *poller) parseToDeviceMessages(res *SyncResponse) {
	if len(res.ToDevice.Events) == 0 {
		return
	}
	p.receiver.AddToDeviceMessages(p.userID, p.deviceID, res.ToDevice.Events)
}

func (p *poller) parseE2EEData(res *SyncResponse) {
	hasE2EEChanges := false
	if res.DeviceListsOTKCount != nil && len(res.DeviceListsOTKCount) > 0 {
		if len(p.otkCounts) != len(res.DeviceListsOTKCount) {
			hasE2EEChanges = true
		}
		if !hasE2EEChanges && p.otkCounts != nil {
			for k := range res.DeviceListsOTKCount {
				if res.DeviceListsOTKCount[k] != p.otkCounts[k] {
					hasE2EEChanges = true
					break
				}
			}
		}
		p.otkCounts = res.DeviceListsOTKCount
	}
	if len(res.DeviceUnusedFallbackKeyTypes) > 0 {
		if !hasE2EEChanges {
			if len(p.fallbackKeyTypes) != len(res.DeviceUnusedFallbackKeyTypes) {
				hasE2EEChanges = true
			} else {
				for i := range res.DeviceUnusedFallbackKeyTypes {
					if res.DeviceUnusedFallbackKeyTypes[i] != p.fallbackKeyTypes[i] {
						hasE2EEChanges = true
						break
					}
				}
			}
		}
		p.fallbackKeyTypes = res.DeviceUnusedFallbackKeyTypes
	}

	deviceListChanges := internal.ToDeviceListChangesMap(res.DeviceLists.Changed, res.DeviceLists.Left)
	if deviceListChanges != nil {
		hasE2EEChanges = true
	}

	if hasE2EEChanges {
		p.receiver.OnE2EEData(p.userID, p.deviceID, p.otkCounts, p.fallbackKeyTypes, deviceListChanges)
	}
}

func (p *poller) parseGlobalAccountData(res *SyncResponse) {
	if len(res.AccountData.Events) == 0 {
		return
	}
	p.receiver.OnAccountData(p.userID, AccountDataGlobalRoom, res.AccountData.Events)
}

func (p *poller) parseRoomsResponse(res *SyncResponse) {
	stateCalls := 0
	timelineCalls := 0
	typingCalls := 0
	receiptCalls := 0
	for roomID, roomData := range res.Rooms.Join {
		if len(roomData.State.Events) > 0 {
			stateCalls++
			p.receiver.Initialise(roomID, roomData.State.Events)
		}
		// process unread counts before events else we might push the event without including said event in the count
		if roomData.UnreadNotifications.HighlightCount != nil || roomData.UnreadNotifications.NotificationCount != nil {
			p.receiver.UpdateUnreadCounts(
				roomID, p.userID, roomData.UnreadNotifications.HighlightCount, roomData.UnreadNotifications.NotificationCount,
			)
		}
		// process typing/receipts before events so we seed the caches correctly for when we return the room
		for _, ephEvent := range roomData.Ephemeral.Events {
			ephEventType := gjson.GetBytes(ephEvent, "type").Str
			switch ephEventType {
			case "m.typing":
				typingCalls++
				p.receiver.SetTyping(roomID, ephEvent)
			case "m.receipt":
				receiptCalls++
				p.receiver.OnReceipt(p.userID, roomID, ephEventType, ephEvent)
			}
		}

		// process account data
		if len(roomData.AccountData.Events) > 0 {
			p.receiver.OnAccountData(p.userID, roomID, roomData.AccountData.Events)
		}
		if len(roomData.Timeline.Events) > 0 {
			timelineCalls++
			p.receiver.Accumulate(p.userID, roomID, roomData.Timeline.PrevBatch, roomData.Timeline.Events)
		}
	}
	for roomID, roomData := range res.Rooms.Leave {
		// TODO: do we care about state?
		if len(roomData.Timeline.Events) > 0 {
			p.receiver.Accumulate(p.userID, roomID, roomData.Timeline.PrevBatch, roomData.Timeline.Events)
		}
		p.receiver.OnLeftRoom(p.userID, roomID)
	}
	for roomID, roomData := range res.Rooms.Invite {
		p.receiver.OnInvite(p.userID, roomID, roomData.InviteState.Events)
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
