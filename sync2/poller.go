package sync2

import (
	"encoding/json"
	"math"
	"os"
	"time"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

// alias time.Sleep so tests can monkey patch it out
var timeSleep = time.Sleep

// Poller can automatically poll the sync v2 endpoint and accumulate the responses in the accumulator
type Poller struct {
	authorizationHeader string
	deviceID            string
	client              clientInterface
	accumulator         accumulatorInterface
	sessions            sessionsInterface
	logger              zerolog.Logger

	// flag set to true when poll() returns due to expired access tokens
	Terminated bool
}

func NewPoller(authHeader, deviceID string, client *Client, accumulator *state.Accumulator, sessions *sync3.Sessions) *Poller {
	return newPoller(authHeader, deviceID, client, accumulator, sessions)
}

func newPoller(authHeader, deviceID string, client clientInterface, accumulator accumulatorInterface, sessions sessionsInterface) *Poller {
	return &Poller{
		authorizationHeader: authHeader,
		deviceID:            deviceID,
		client:              client,
		accumulator:         accumulator,
		sessions:            sessions,
		Terminated:          false,
		logger: zerolog.New(os.Stdout).With().Timestamp().Logger().With().Str("device", deviceID).Logger().Output(zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: "15:04:05",
		}),
	}
}

// Poll will block forever, repeatedly calling v2 sync. Do this in a goroutine.
// Returns if the access token gets invalidated. Invokes the callback on first success.
func (p *Poller) Poll(since string, callback func()) {
	p.logger.Info().Str("since", since).Msg("v2 poll loop started")
	failCount := 0
	firstTime := true
	for {
		if failCount > 0 {
			waitTime := time.Duration(math.Pow(2, float64(failCount))) * time.Second
			p.logger.Warn().Str("duration", waitTime.String()).Msg("waiting before next poll")
			timeSleep(waitTime)
		}
		p.logger.Info().Str("since", since).Msg("requesting data")
		resp, statusCode, err := p.client.DoSyncV2(p.authorizationHeader, since)
		if err != nil {
			// check if temporary
			if statusCode != 401 {
				p.logger.Warn().Int("code", statusCode).Err(err).Msg("sync v2 poll returned temporary error")
				failCount += 1
				continue
			} else {
				p.logger.Warn().Msg("access token has been invalidated, terminating loop")
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
			p.logger.Warn().Str("since", since).Err(err).Msg("failed to persist new since value")
		}

		if firstTime {
			firstTime = false
			callback()
		}
	}
}

func (p *Poller) accumulate(res *SyncResponse) {
	if len(res.Rooms.Join) == 0 {
		return
	}
	for roomID, roomData := range res.Rooms.Join {
		if len(roomData.State.Events) > 0 {
			err := p.accumulator.Initialise(roomID, roomData.State.Events)
			if err != nil {
				p.logger.Err(err).Str("room_id", roomID).Int("num_state_events", len(roomData.State.Events)).Msg("Accumulator.Initialise failed")
			}
		}
		err := p.accumulator.Accumulate(roomID, roomData.Timeline.Events)
		if err != nil {
			p.logger.Err(err).Str("room_id", roomID).Int("num_timeline_events", len(roomData.Timeline.Events)).Msg("Accumulator.Accumulate failed")
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
				err = p.accumulator.SetTyping(roomID, userIDs)
				if err != nil {
					p.logger.Err(err).Str("room_id", roomID).Strs("user_ids", userIDs).Msg("Accumulator: failed to set typing")
				}
			}
		}
	}
	p.logger.Info().Int("num_rooms", len(res.Rooms.Join)).Msg("accumulated data")
}

// the subset of Sessions which the poller uses, mocked for tests
type sessionsInterface interface {
	UpdateDeviceSince(deviceID, since string) error
}

// the subset of Client which the poller uses, mocked for tests
type clientInterface interface {
	DoSyncV2(authHeader, since string) (*SyncResponse, int, error)
}

// the subset of Accumulator which the poller uses, mocked for tests
type accumulatorInterface interface {
	Accumulate(roomID string, timeline []json.RawMessage) error
	Initialise(roomID string, state []json.RawMessage) error
	SetTyping(roomID string, userIDs []string) error
}
