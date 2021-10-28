package sync2

import (
	"encoding/json"
	"math"
	"sync"
	"time"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

// alias time.Sleep so tests can monkey patch it out
var timeSleep = time.Sleep

// V2DataReceiver is the receiver for all the v2 sync data the poller gets
type V2DataReceiver interface {
	UpdateDeviceSince(deviceID, since string)
	Accumulate(roomID string, timeline []json.RawMessage)
	Initialise(roomID string, state []json.RawMessage)
	SetTyping(roomID string, userIDs []string)
	// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
	// would implicitly acknowledge these messages.
	AddToDeviceMessages(userID, deviceID string, msgs []gomatrixserverlib.SendToDeviceEvent)

	UpdateUnreadCounts(roomID, userID string, highlightCount, notifCount *int)
}

// PollerMap is a map of device ID to Poller
type PollerMap struct {
	v2Client        Client
	callbacks       V2DataReceiver
	pollerMu        *sync.Mutex
	Pollers         map[string]*Poller // device_id -> poller
	executor        chan func()
	executorRunning bool
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
	}
}

// EnsurePolling makes sure there is a poller for this device, making one if need be.
// Blocks until at least 1 sync is done if and only if the poller was just created.
// This ensures that calls to the database will return data.
// Guarantees only 1 poller will be running per deviceID.
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
		return
	}
	// replace the poller
	poller = NewPoller(userID, authHeader, deviceID, h.v2Client, h, logger)
	var wg sync.WaitGroup
	wg.Add(1)
	go poller.Poll(v2since, func() {
		wg.Done()
	})
	h.Pollers[deviceID] = poller
	h.pollerMu.Unlock()
	wg.Wait()
}

func (h *PollerMap) execute() {
	for fn := range h.executor {
		fn()
	}
}

func (h *PollerMap) UpdateDeviceSince(deviceID, since string) {
	h.callbacks.UpdateDeviceSince(deviceID, since)
}
func (h *PollerMap) Accumulate(roomID string, timeline []json.RawMessage) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.executor <- func() {
		h.callbacks.Accumulate(roomID, timeline)
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

// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
// would implicitly acknowledge these messages.
func (h *PollerMap) AddToDeviceMessages(userID, deviceID string, msgs []gomatrixserverlib.SendToDeviceEvent) {
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

// Poller can automatically poll the sync v2 endpoint and accumulate the responses in storage
type Poller struct {
	userID              string
	authorizationHeader string
	deviceID            string
	client              Client
	receiver            V2DataReceiver
	logger              zerolog.Logger

	// flag set to true when poll() returns due to expired access tokens
	Terminated bool
}

func NewPoller(userID, authHeader, deviceID string, client Client, receiver V2DataReceiver, logger zerolog.Logger) *Poller {
	return &Poller{
		authorizationHeader: authHeader,
		userID:              userID,
		deviceID:            deviceID,
		client:              client,
		receiver:            receiver,
		Terminated:          false,
		logger:              logger,
	}
}

// Poll will block forever, repeatedly calling v2 sync. Do this in a goroutine.
// Returns if the access token gets invalidated or if there was a fatal error processing v2 resposnes.
// Invokes the callback on first success.
func (p *Poller) Poll(since string, callback func()) {
	p.logger.Info().Str("since", since).Msg("Poller: v2 poll loop started")
	failCount := 0
	firstTime := true
	for {
		if failCount > 0 {
			waitTime := time.Duration(math.Pow(2, float64(failCount))) * time.Second
			p.logger.Warn().Str("duration", waitTime.String()).Msg("Poller: waiting before next poll")
			timeSleep(waitTime)
		}
		resp, statusCode, err := p.client.DoSyncV2(p.authorizationHeader, since)
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
		p.parseRoomsResponse(resp)
		p.parseToDeviceMessages(resp)

		since = resp.NextBatch
		// persist the since token (TODO: this could get slow if we hammer the DB too much)
		p.receiver.UpdateDeviceSince(p.deviceID, since)

		if firstTime {
			firstTime = false
			callback()
		}
	}
}

func (p *Poller) parseToDeviceMessages(res *SyncResponse) {
	if len(res.ToDevice.Events) == 0 {
		return
	}
	p.receiver.AddToDeviceMessages(p.userID, p.deviceID, res.ToDevice.Events)
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
		if len(roomData.Timeline.Events) > 0 {
			timelineCalls++
			p.receiver.Accumulate(roomID, roomData.Timeline.Events)
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
			p.receiver.Accumulate(roomID, roomData.Timeline.Events)
		}
	}
	p.logger.Info().Ints(
		"rooms [invite,join,leave]", []int{len(res.Rooms.Invite), len(res.Rooms.Join), len(res.Rooms.Leave)},
	).Ints(
		"storage [states,timelines,typing]", []int{stateCalls, timelineCalls, typingCalls},
	).Msg("Poller: accumulated data")
}
