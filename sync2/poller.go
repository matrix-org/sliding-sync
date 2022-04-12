package sync2

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

// alias time.Sleep so tests can monkey patch it out
var timeSleep = time.Sleep

// V2DataReceiver is the receiver for all the v2 sync data the poller gets
type V2DataReceiver interface {
	UpdateDeviceSince(deviceID, since string)
	Accumulate(roomID, prevBatch string, timeline []json.RawMessage)
	Initialise(roomID string, state []json.RawMessage)
	SetTyping(roomID string, userIDs []string)
	// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
	// would implicitly acknowledge these messages.
	AddToDeviceMessages(userID, deviceID string, msgs []json.RawMessage)

	UpdateUnreadCounts(roomID, userID string, highlightCount, notifCount *int)

	OnAccountData(userID, roomID string, events []json.RawMessage)
	OnInvite(userID, roomID string, inviteState []json.RawMessage)
	OnRetireInvite(userID, roomID string)
}

// Fetcher which PollerMap satisfies used by the E2EE extension
type E2EEFetcher interface {
	LatestE2EEData(deviceID string) (otkCounts map[string]int, changed, left []string)
}

type TransactionIDFetcher interface {
	TransactionIDForEvent(userID, eventID string) (txnID string)
}

// PollerMap is a map of device ID to Poller
type PollerMap struct {
	v2Client        Client
	callbacks       V2DataReceiver
	pollerMu        *sync.Mutex
	Pollers         map[string]*Poller // device_id -> poller
	executor        chan func()
	executorRunning bool
	txnCache        *TransactionIDCache
}

// NewPollerMap makes a new PollerMap. Guarantees that the V2DataReceiver will be called on the same
// goroutine for all pollers. This is required to avoid race conditions at the Go level. Whilst we
// use SQL transactions to ensure that the DB doesn't race, we then subsequently feed new events
// from that call into a global cache. This can race which can result in out of order latest NIDs
// which, if we assert NIDs only increment, will result in missed events.
//
// Consider these events in the same room, with 3 different pollers getting the data:
//   1 2 3 4 5 6 7 eventual DB event NID
//   A B C D E F G
//   -----          poll loop 1 = A,B,C          new events = A,B,C latest=3
//   ---------      poll loop 2 = A,B,C,D,E      new events = D,E   latest=5
//   -------------  poll loop 3 = A,B,C,D,E,F,G  new events = F,G   latest=7
// The DB layer will correctly assign NIDs and stop duplicates, resulting in a set of new events which
// do not overlap. However, there is a gap between this point and updating the cache, where variable
// delays can be introduced, so F,G latest=7 could be injected first. If we then never walk back to
// earlier NIDs, A,B,C,D,E will be dropped from the cache.
//
// This only affects resources which are shared across multiple DEVICES such as:
//   - room resources: events, EDUs
//   - user resources: notif counts, account data
// NOT to-device messages,or since tokens.
func NewPollerMap(v2Client Client, callbacks V2DataReceiver) *PollerMap {
	return &PollerMap{
		v2Client:  v2Client,
		callbacks: callbacks,
		pollerMu:  &sync.Mutex{},
		Pollers:   make(map[string]*Poller),
		executor:  make(chan func(), 0),
		txnCache:  NewTransactionIDCache(),
	}
}

// TransactionIDForEvent returns the transaction ID for this event for this user, if one exists.
func (h *PollerMap) TransactionIDForEvent(userID, eventID string) string {
	return h.txnCache.Get(userID, eventID)
}

// LatestE2EEData pulls the latest device_lists and device_one_time_keys_count values from the poller.
// These bits of data are ephemeral and do not need to be persisted.
func (h *PollerMap) LatestE2EEData(deviceID string) (otkCounts map[string]int, changed, left []string) {
	h.pollerMu.Lock()
	poller := h.Pollers[deviceID]
	h.pollerMu.Unlock()
	if poller == nil || poller.Terminated {
		// possible if we have 2 devices for the same user, we just need to
		// wait a bit for the 2nd device's v2 /sync to return
		return
	}
	otkCounts = poller.OTKCounts()
	changed, left = poller.DeviceListChanges()
	return
}

// EnsurePolling makes sure there is a poller for this user, making one if need be.
// Blocks until at least 1 sync is done if and only if the poller was just created.
// This ensures that calls to the database will return data.
// Guarantees only 1 poller will be running per deviceID.
// Note that we will immediately return if there is a poller for the same user but a different device.
// We do this to allow for logins on clients to be snappy fast, even though they won't yet have the
// to-device msgs to decrypt E2EE roms.
func (h *PollerMap) EnsurePolling(authHeader, userID, deviceID, v2since string, logger zerolog.Logger) {
	h.pollerMu.Lock()
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
	poller = NewPoller(userID, authHeader, deviceID, h.v2Client, h, h.txnCache, logger)
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
func (h *PollerMap) Accumulate(roomID, prevBatch string, timeline []json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.Accumulate(roomID, prevBatch, timeline)
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
func (h *PollerMap) SetTyping(roomID string, userIDs []string) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.SetTyping(roomID, userIDs)
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

func (h *PollerMap) OnRetireInvite(userID, roomID string) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.OnRetireInvite(userID, roomID)
		wg.Done()
	}
	wg.Wait()
}

// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
// would implicitly acknowledge these messages.
func (h *PollerMap) AddToDeviceMessages(userID, deviceID string, msgs []json.RawMessage) {
	h.callbacks.AddToDeviceMessages(userID, deviceID, msgs)
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

// Poller can automatically poll the sync v2 endpoint and accumulate the responses in storage
type Poller struct {
	userID              string
	authorizationHeader string
	deviceID            string
	client              Client
	receiver            V2DataReceiver
	logger              zerolog.Logger

	// remember txn ids
	txnCache *TransactionIDCache

	// E2EE fields
	e2eeMu            *sync.Mutex
	otkCounts         map[string]int
	deviceListChanges map[string]string // latest user_id -> state e.g "@alice" -> "left"

	// flag set to true when poll() returns due to expired access tokens
	Terminated bool
	wg         *sync.WaitGroup
}

func NewPoller(userID, authHeader, deviceID string, client Client, receiver V2DataReceiver, txnCache *TransactionIDCache, logger zerolog.Logger) *Poller {
	var wg sync.WaitGroup
	wg.Add(1)
	return &Poller{
		authorizationHeader: authHeader,
		userID:              userID,
		deviceID:            deviceID,
		client:              client,
		receiver:            receiver,
		Terminated:          false,
		logger:              logger,
		e2eeMu:              &sync.Mutex{},
		deviceListChanges:   make(map[string]string),
		wg:                  &wg,
		txnCache:            txnCache,
	}
}

// Blocks until the initial sync has been done on this poller.
func (p *Poller) WaitUntilInitialSync() {
	p.wg.Wait()
}

// Poll will block forever, repeatedly calling v2 sync. Do this in a goroutine.
// Returns if the access token gets invalidated or if there was a fatal error processing v2 responses.
// Use WaitUntilInitialSync() to wait until the first poll has been processed.
func (p *Poller) Poll(since string) {
	p.logger.Info().Str("since", since).Msg("Poller: v2 poll loop started")
	failCount := 0
	firstTime := true
	for {
		if failCount > 0 {
			// don't backoff when doing v2 syncs because the response is only in the cache for a short
			// period of time (on massive accounts on matrix.org) such that if you wait 2,4,8min between
			// requests it might force the server to do the work all over again :(
			waitTime := 3 * time.Second
			p.logger.Warn().Str("duration", waitTime.String()).Int("fail-count", failCount).Msg("Poller: waiting before next poll")
			timeSleep(waitTime)
		}
		resp, statusCode, err := p.client.DoSyncV2(p.authorizationHeader, since, firstTime)
		if err != nil {
			// check if temporary
			if statusCode != 401 {
				p.logger.Warn().Int("code", statusCode).Err(err).Msg("Poller: sync v2 poll returned temporary error")
				failCount += 1
				continue
			} else {
				p.logger.Warn().Msg("Poller: access token has been invalidated, terminating loop")
				p.Terminated = true
				return
			}
		}
		failCount = 0
		p.parseE2EEData(resp)
		p.parseGlobalAccountData(resp)
		p.parseRoomsResponse(resp)
		p.parseToDeviceMessages(resp)

		since = resp.NextBatch
		// persist the since token (TODO: this could get slow if we hammer the DB too much)
		p.receiver.UpdateDeviceSince(p.deviceID, since)

		if firstTime {
			firstTime = false
			p.wg.Done()
		}
	}
}

func (p *Poller) OTKCounts() map[string]int {
	p.e2eeMu.Lock()
	defer p.e2eeMu.Unlock()
	return p.otkCounts
}

func (p *Poller) DeviceListChanges() (changed, left []string) {
	p.e2eeMu.Lock()
	defer p.e2eeMu.Unlock()
	for userID, state := range p.deviceListChanges {
		switch state {
		case "changed":
			changed = append(changed, userID)
		case "left":
			left = append(left, userID)
		default:
			p.logger.Warn().Str("state", state).Msg("DeviceListChanges: unknown state")
		}
	}
	// forget them so we don't send them more than once to v3 loops
	p.deviceListChanges = map[string]string{}
	return
}

func (p *Poller) parseToDeviceMessages(res *SyncResponse) {
	if len(res.ToDevice.Events) == 0 {
		return
	}
	p.receiver.AddToDeviceMessages(p.userID, p.deviceID, res.ToDevice.Events)
}

func (p *Poller) parseE2EEData(res *SyncResponse) {
	p.e2eeMu.Lock()
	defer p.e2eeMu.Unlock()
	// we don't actively push this to v3 loops, we let them lazily fetch it via calls to
	// Poller.DeviceListChanges() and Poller.OTKCounts()
	if res.DeviceListsOTKCount != nil {
		p.otkCounts = res.DeviceListsOTKCount
	}
	for _, userID := range res.DeviceLists.Changed {
		p.deviceListChanges[userID] = "changed"
	}
	for _, userID := range res.DeviceLists.Left {
		p.deviceListChanges[userID] = "left"
	}
}

func (p *Poller) parseGlobalAccountData(res *SyncResponse) {
	if len(res.AccountData.Events) == 0 {
		return
	}
	p.receiver.OnAccountData(p.userID, AccountDataGlobalRoom, res.AccountData.Events)
}

func (p *Poller) updateTxnIDCache(timeline []json.RawMessage) {
	for _, e := range timeline {
		txnID := gjson.GetBytes(e, "unsigned.transaction_id")
		if !txnID.Exists() {
			continue
		}
		eventID := gjson.GetBytes(e, "event_id").Str
		p.txnCache.Store(p.userID, eventID, txnID.Str)
	}
}

func (p *Poller) parseRoomsResponse(res *SyncResponse) {
	stateCalls := 0
	timelineCalls := 0
	typingCalls := 0
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
		// process account data
		if len(roomData.AccountData.Events) > 0 {
			p.receiver.OnAccountData(p.userID, roomID, roomData.AccountData.Events)
		}
		if len(roomData.Timeline.Events) > 0 {
			timelineCalls++
			p.updateTxnIDCache(roomData.Timeline.Events)
			p.receiver.Accumulate(roomID, roomData.Timeline.PrevBatch, roomData.Timeline.Events)
		}
		for _, ephEvent := range roomData.Ephemeral.Events {
			if gjson.GetBytes(ephEvent, "type").Str == "m.typing" {
				users := gjson.GetBytes(ephEvent, "content.user_ids")
				if !users.IsArray() {
					continue // malformed event
				}
				var userIDs []string
				for _, u := range users.Array() {
					if u.Str != "" {
						userIDs = append(userIDs, u.Str)
					}
				}
				typingCalls++
				p.receiver.SetTyping(roomID, userIDs)
			}
		}
	}
	for roomID, roomData := range res.Rooms.Leave {
		// TODO: do we care about state?

		if len(roomData.Timeline.Events) > 0 {
			p.receiver.Accumulate(roomID, roomData.Timeline.PrevBatch, roomData.Timeline.Events)
		}
		p.receiver.OnRetireInvite(p.userID, roomID)
	}
	for roomID, roomData := range res.Rooms.Invite {
		p.receiver.OnInvite(p.userID, roomID, roomData.InviteState.Events)
	}
	var l *zerolog.Event
	if len(res.Rooms.Invite) > 1 || len(res.Rooms.Join) > 1 {
		l = p.logger.Info()
	} else {
		l = p.logger.Debug()
	}
	l.Ints(
		"rooms [invite,join,leave]", []int{len(res.Rooms.Invite), len(res.Rooms.Join), len(res.Rooms.Leave)},
	).Ints(
		"storage [states,timelines,typing]", []int{stateCalls, timelineCalls, typingCalls},
	).Int("to_device", len(res.ToDevice.Events)).Msg("Poller: accumulated data")
}
