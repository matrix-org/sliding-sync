package delta

import (
	"time"

	"github.com/matrix-org/sliding-sync/state"
)

type ManagerInterface interface {
	AsyncLoadDeltaState(deltaToken string, createNewToken bool) <-chan *State
	WaitFor(asyncState <-chan *State) *State
}

// Manager manages which delta states are loaded, handles async loading of delta states, and
// manages persistence in the DB periodically.
type Manager struct {
	store *state.Storage
}

func NewManager(store *state.Storage) *Manager {
	return &Manager{
		store: store,
	}
}

func (m *Manager) AsyncLoadDeltaState(deltaToken string, createNewToken bool) <-chan *State {
	ch := make(chan *State)
	close(ch)
	return ch
}

func (m *Manager) WaitFor(asyncState <-chan *State) *State {
	timeout := 5 * time.Second
	select {
	case <-time.After(timeout):
		return nil
	case s := <-asyncState:
		return s
	}
}
