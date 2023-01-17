package delta

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fxamacker/cbor/v2"
	"github.com/matrix-org/sliding-sync/state"
)

// State is the delta cache for a single scope. It implements the bandwidth optimisations described
// in https://github.com/matrix-org/matrix-spec-proposals/blob/kegan/sync-v3/proposals/3575-sync.md#bandwidth-optimisations-for-persistent-clients
type State struct {
	ID       int64  // pkey for persistence, like a session id
	DeviceID string // scope to prevent other users using another user's delta token
	Data     *StateData
}

func NewStateFromRow(row *state.DeltaRow) (*State, error) {
	var data StateData
	if len(row.Data) > 0 {
		err := cbor.Unmarshal(row.Data, &data)
		if err != nil {
			return nil, err
		}
	}
	return &State{
		ID:       row.ID,
		DeviceID: row.DeviceID,
		Data:     &data,
	}, nil
}

type StateData struct {
	Position int64        `cbor:"1,keyasint,omitempty"`
	Rooms    DeltaRooms   `cbor:"2,keyasint,omitempty"`
	Account  DeltaAccount `cbor:"3,keyasint,omitempty"`
}

func (d *StateData) CBOR() ([]byte, error) {
	return cbor.Marshal(d)
}

// DeltaRooms contains delta tokens for room data
type DeltaRooms map[string]struct {
	RequiredState map[string]int64 `cbor:"1,keyasint,omitempty"`
}

// DeltaAccount contains delta tokens for account data
type DeltaAccount struct {
	AccountID int64 `cbor:"1,keyasint,omitempty"`
}

// AddAccountData to the delta struct
func (d *State) AddAccountData(accountDatas []state.AccountData) (filteredAccountDatas []state.AccountData) {
	d.Data.Position++
	return
}

func (d *State) Token() string {
	return fmt.Sprintf("%d_%d", d.ID, d.Data.Position)
}

func parseToken(token string) (id, pos int64, err error) {
	segments := strings.Split(token, "_")
	if len(segments) != 2 {
		return 0, 0, fmt.Errorf("invalid token: %s", token)
	}
	id, err = strconv.ParseInt(segments[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	pos, err = strconv.ParseInt(segments[1], 10, 64)
	return
}
