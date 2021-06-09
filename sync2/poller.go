package sync2

import (
	"math"
	"os"
	"time"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/rs/zerolog"
)

// Poller can automatically poll the sync v2 endpoint and accumulate the responses in the accumulator
type Poller struct {
	AuthorizationHeader string
	DeviceID            string
	Client              *Client
	Accumulator         *state.Accumulator
	Sessions            *sync3.Sessions
	// flag set to true when poll() returns due to expired access tokens
	Terminated bool
	logger     zerolog.Logger
}

func NewPoller(authHeader, deviceID string, client *Client, accumulator *state.Accumulator, sessions *sync3.Sessions) *Poller {
	return &Poller{
		AuthorizationHeader: authHeader,
		DeviceID:            deviceID,
		Client:              client,
		Accumulator:         accumulator,
		Sessions:            sessions,
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
			time.Sleep(waitTime)
		}
		p.logger.Info().Str("since", since).Msg("requesting data")
		resp, statusCode, err := p.Client.DoSyncV2(p.AuthorizationHeader, since)
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
		err = p.Sessions.UpdateDeviceSince(p.DeviceID, since)
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
			err := p.Accumulator.Initialise(roomID, roomData.State.Events)
			if err != nil {
				p.logger.Err(err).Str("room_id", roomID).Int("num_state_events", len(roomData.State.Events)).Msg("Accumulator.Initialise failed")
			}
		}
		err := p.Accumulator.Accumulate(roomID, roomData.Timeline.Events)
		if err != nil {
			p.logger.Err(err).Str("room_id", roomID).Int("num_timeline_events", len(roomData.Timeline.Events)).Msg("Accumulator.Accumulate failed")
		}
	}
	p.logger.Info().Int("num_rooms", len(res.Rooms.Join)).Msg("accumulated data")
}
