package delta

import (
	"fmt"
	"os"
	"time"

	"github.com/matrix-org/sliding-sync/state"
	"github.com/rs/zerolog"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

type ManagerInterface interface {
	AsyncLoadDeltaState(deviceID string, deltaToken string, createNewToken bool) <-chan *State
	WaitFor(asyncState <-chan *State) *State
}

// Manager manages which delta states are loaded, handles async loading of delta states, and
// manages persistence in the DB periodically.
type Manager struct {
	store state.DeltaTableInterface
}

func NewManager(store state.DeltaTableInterface) *Manager {
	return &Manager{
		store: store,
	}
}

// AsyncLoadDeltaState loads the delta state into memory if it isn't already. Returns a channel which
// will be sent the delta.State once the load has finished.
func (m *Manager) AsyncLoadDeltaState(deviceID string, deltaToken string, createNewToken bool) <-chan *State {
	// TODO: check in-memory first
	ch := make(chan *State, 1)
	go func() {
		defer close(ch)
		var row *state.DeltaRow
		var err error
		if createNewToken {
			row, err = m.store.CreateDeltaState(deviceID)
		} else if deltaToken != "" {
			var id int64
			id, _, err = parseToken(deltaToken)
			if err == nil {
				row, err = m.store.Load(id, deviceID)
			}
		} else {
			return // need either create/delta token set
		}

		if err != nil {
			logger.Err(err).Str("device", deviceID).Str("token", deltaToken).Bool("create", createNewToken).Msg(
				"failed to load delta state",
			)
			return
		}
		state, err := NewStateFromRow(row)
		if err != nil {
			logger.Err(err).Str("device", deviceID).Str("token", deltaToken).Bool("create", createNewToken).Msg(
				"failed to NewStateFromRow",
			)
			return
		}
		// TODO: cache in memory
		ch <- state
	}()
	return ch
}

// WaitFor a delta.State from AsyncLoadDeltaState. It isn't safe to call this function more than once.
// May return nil if the load takes > timeout seconds.
func (m *Manager) WaitFor(asyncState <-chan *State) *State {
	timeout := 5 * time.Second
	select {
	case <-time.After(timeout):
		return nil
	case s := <-asyncState:
		return s
	}
}

func (m *Manager) Save(s *State) error {
	data, err := s.Data.CBOR()
	if err != nil {
		return fmt.Errorf("failed to generate CBOR for delta: %v", err)
	}
	err = m.store.Update(&state.DeltaRow{
		ID:       s.ID,
		DeviceID: s.DeviceID,
		Data:     data,
	})
	if err != nil {
		return fmt.Errorf("failed to DeltaTable.Update: %v", err)
	}
	return nil
}
