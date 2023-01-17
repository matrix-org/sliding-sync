package delta

import (
	"fmt"

	"github.com/matrix-org/sliding-sync/state"
)

// State is the delta cache for a single scope. It implements the bandwidth optimisations described
// in https://github.com/matrix-org/matrix-spec-proposals/blob/kegan/sync-v3/proposals/3575-sync.md#bandwidth-optimisations-for-persistent-clients
type State struct {
	ID       int64 // pkey for persistence
	Position int64
	DeviceID string // scope to prevent other users using another user's delta token
	Rooms    DeltaRooms
	Account  DeltaAccount
}

func NewState() *State {
	return &State{
		Rooms:   make(DeltaRooms),
		Account: DeltaAccount{},
	}
}

// DeltaRooms contains delta tokens for room data
type DeltaRooms map[string]struct {
	RequiredState map[string]int64
}

// DeltaAccount contains delta tokens for account data
type DeltaAccount struct {
	AccountID int64
}

// Advance the delta struct
func (d *State) Advance(accountDatas []state.AccountData) (filteredAccountDatas []state.AccountData, deltaToken int64) {
	d.Position++
	return
}

func (d *State) Token() string {
	return fmt.Sprintf("%s_%d_%d", d.DeviceID, d.ID, d.Position)
}
