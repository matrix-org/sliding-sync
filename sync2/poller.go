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
	UpdateDeviceSince(deviceID, since string) error
	Accumulate(roomID string, timeline []json.RawMessage) error
	Initialise(roomID string, state []json.RawMessage) error
	SetTyping(roomID string, userIDs []string) (int64, error)
	// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
	// would implicitly acknowledge these messages.
	AddToDeviceMessages(userID, deviceID string, msgs []gomatrixserverlib.SendToDeviceEvent) error

	UpdateUnreadCounts(roomID, userID string, highlightCount, notifCount *int)
}

// PollerMap is a map of device ID to Poller
type PollerMap struct {
	v2Client  Client
	callbacks V2DataReceiver
	pollerMu  *sync.Mutex
	Pollers   map[string]*Poller // device_id -> poller
}

func NewPollerMap(v2Client Client, callbacks V2DataReceiver) *PollerMap {
	return &PollerMap{
		v2Client:  v2Client,
		callbacks: callbacks,
		pollerMu:  &sync.Mutex{},
		Pollers:   make(map[string]*Poller),
	}
}

// EnsurePolling makes sure there is a poller for this device, making one if need be.
// Blocks until at least 1 sync is done if and only if the poller was just created.
// This ensures that calls to the database will return data.
// Guarantees only 1 poller will be running per deviceID.
func (h *PollerMap) EnsurePolling(authHeader, userID, deviceID, v2since string, logger zerolog.Logger) {
	h.pollerMu.Lock()
	poller, ok := h.Pollers[deviceID]
	// a poller exists and hasn't been terminated so we don't need to do anything
	if ok && !poller.Terminated {
		h.pollerMu.Unlock()
		return
	}
	// replace the poller
	poller = NewPoller(userID, authHeader, deviceID, h.v2Client, h.callbacks, logger)
	var wg sync.WaitGroup
	wg.Add(1)
	go poller.Poll(v2since, func() {
		wg.Done()
	})
	h.Pollers[deviceID] = poller
	h.pollerMu.Unlock()
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
		if err = p.parseToDeviceMessages(resp); err != nil {
			p.logger.Err(err).Str("since", since).Msg("Poller: V2DataReceiver failed to persist to-device messages. Terminating loop.")
			p.Terminated = true
			return
		}
		since = resp.NextBatch
		// persist the since token (TODO: this could get slow if we hammer the DB too much)
		err = p.receiver.UpdateDeviceSince(p.deviceID, since)
		if err != nil {
			// non-fatal
			p.logger.Warn().Str("since", since).Err(err).Msg("Poller: V2DataReceiver failed to persist new since value")
		}

		if firstTime {
			firstTime = false
			callback()
		}
	}
}

func (p *Poller) parseToDeviceMessages(res *SyncResponse) error {
	if len(res.ToDevice.Events) == 0 {
		return nil
	}
	return p.receiver.AddToDeviceMessages(p.userID, p.deviceID, res.ToDevice.Events)
}

func (p *Poller) parseRoomsResponse(res *SyncResponse) {
	stateCalls := 0
	timelineCalls := 0
	typingCalls := 0
	for roomID, roomData := range res.Rooms.Join {
		if len(roomData.State.Events) > 0 {
			stateCalls++
			err := p.receiver.Initialise(roomID, roomData.State.Events)
			if err != nil {
				p.logger.Err(err).Str("room_id", roomID).Int("num_state_events", len(roomData.State.Events)).Msg("Poller: V2DataReceiver.Initialise failed")
			}
		}
		if len(roomData.Timeline.Events) > 0 {
			timelineCalls++
			err := p.receiver.Accumulate(roomID, roomData.Timeline.Events)
			if err != nil {
				p.logger.Err(err).Str("room_id", roomID).Int("num_timeline_events", len(roomData.Timeline.Events)).Msg("Poller: V2DataReceiver.Accumulate failed")
			}
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
				_, err := p.receiver.SetTyping(roomID, userIDs)
				if err != nil {
					p.logger.Err(err).Str("room_id", roomID).Strs("user_ids", userIDs).Msg("Poller: V2DataReceiver failed to SetTyping")
				}
			}
		}
		if roomData.UnreadNotifications.HighlightCount != nil || roomData.UnreadNotifications.NotificationCount != nil {
			p.receiver.UpdateUnreadCounts(
				roomID, p.userID, roomData.UnreadNotifications.HighlightCount, roomData.UnreadNotifications.NotificationCount,
			)
		}
	}
	for roomID, roomData := range res.Rooms.Leave {
		// TODO: do we care about state?

		if len(roomData.Timeline.Events) > 0 {
			err := p.receiver.Accumulate(roomID, roomData.Timeline.Events)
			if err != nil {
				p.logger.Err(err).Str("room_id", roomID).Int("num_timeline_events", len(roomData.Timeline.Events)).Msg("Poller: V2DataReceiver.Accumulate left room failed")
			}
		}
	}
	p.logger.Info().Ints(
		"rooms [invite,join,leave]", []int{len(res.Rooms.Invite), len(res.Rooms.Join), len(res.Rooms.Leave)},
	).Ints(
		"storage [states,timelines,typing]", []int{stateCalls, timelineCalls, typingCalls},
	).Msg("Poller: accumulated data")
}
