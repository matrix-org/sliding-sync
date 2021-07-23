package sync2

import (
	"encoding/json"
	"math"
	"time"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

// alias time.Sleep so tests can monkey patch it out
var timeSleep = time.Sleep

// Poller can automatically poll the sync v2 endpoint and accumulate the responses in storage
type Poller struct {
	authorizationHeader string
	deviceID            string
	client              Client
	storage             storageInterface
	sessions            sessionsInterface
	logger              zerolog.Logger

	// flag set to true when poll() returns due to expired access tokens
	Terminated bool
}

func NewPoller(authHeader, deviceID string, client Client, storage *state.Storage, sessions *sync3.Sessions, logger zerolog.Logger) *Poller {
	return newPoller(authHeader, deviceID, client, storage, sessions, logger)
}

func newPoller(authHeader, deviceID string, client Client, storage storageInterface, sessions sessionsInterface, logger zerolog.Logger) *Poller {
	return &Poller{
		authorizationHeader: authHeader,
		deviceID:            deviceID,
		client:              client,
		storage:             storage,
		sessions:            sessions,
		Terminated:          false,
		logger:              logger,
	}
}

// Poll will block forever, repeatedly calling v2 sync. Do this in a goroutine.
// Returns if the access token gets invalidated. Invokes the callback on first success.
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
		p.logger.Info().Str("since", since).Msg("Poller: requesting data")
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
		p.accumulate(resp)
		since = resp.NextBatch
		// persist the since token (TODO: this could get slow if we hammer the DB too much)
		err = p.sessions.UpdateDeviceSince(p.deviceID, since)
		if err != nil {
			// non-fatal
			p.logger.Warn().Str("since", since).Err(err).Msg("Poller: failed to persist new since value")
		}

		if firstTime {
			firstTime = false
			callback()
		}
	}
}

func (p *Poller) accumulate(res *SyncResponse) {
	if len(res.Rooms.Join) == 0 {
		p.logger.Info().Msg("Poller: no rooms in join response")
		return
	}
	initCalls := 0
	accumCalls := 0
	typingCalls := 0
	for roomID, roomData := range res.Rooms.Join {
		if len(roomData.State.Events) > 0 {
			initCalls++
			err := p.storage.Initialise(roomID, roomData.State.Events)
			if err != nil {
				p.logger.Err(err).Str("room_id", roomID).Int("num_state_events", len(roomData.State.Events)).Msg("Poller: Accumulator.Initialise failed")
			}
		}
		if len(roomData.Timeline.Events) > 0 {
			accumCalls++
			err := p.storage.Accumulate(roomID, roomData.Timeline.Events)
			if err != nil {
				p.logger.Err(err).Str("room_id", roomID).Int("num_timeline_events", len(roomData.Timeline.Events)).Msg("Poller: Accumulator.Accumulate failed")
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
				_, err := p.storage.SetTyping(roomID, userIDs)
				if err != nil {
					p.logger.Err(err).Str("room_id", roomID).Strs("user_ids", userIDs).Msg("Poller: failed to SetTyping")
				}
			}
		}
	}
	p.logger.Info().Ints(
		"rooms [invite,join,leave]", []int{len(res.Rooms.Invite), len(res.Rooms.Join), len(res.Rooms.Leave)},
	).Ints(
		"storage [inits,accum,typing]", []int{initCalls, accumCalls, typingCalls},
	).Msg("Poller: accumulated data")
}

// the subset of Sessions which the poller uses, mocked for tests
type sessionsInterface interface {
	UpdateDeviceSince(deviceID, since string) error
}

// the subset of Storage which the poller uses, mocked for tests
type storageInterface interface {
	Accumulate(roomID string, timeline []json.RawMessage) error
	Initialise(roomID string, state []json.RawMessage) error
	SetTyping(roomID string, userIDs []string) (int64, error)
}
