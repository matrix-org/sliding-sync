package extensions

import (
	"encoding/json"

	"github.com/matrix-org/sync-v3/state"
)

// Client created request params
type AccountDataRequest struct {
	Enabled                bool             `json:"enabled"`
	GlobalAccountDataTypes []string         `json:"global_account_data_types"`
	RoomAccountDataTypes   map[int][]string `json:"room_account_data_types"`
}

func (r AccountDataRequest) ApplyDelta(next *AccountDataRequest) *AccountDataRequest {
	r.Enabled = next.Enabled
	if next.GlobalAccountDataTypes != nil {
		r.GlobalAccountDataTypes = next.GlobalAccountDataTypes
	}
	if next.RoomAccountDataTypes != nil {
		if r.RoomAccountDataTypes == nil {
			r.RoomAccountDataTypes = make(map[int][]string)
		}
		for listIndex, types := range next.RoomAccountDataTypes {
			r.RoomAccountDataTypes[listIndex] = types
		}
	}
	return &r
}

// Server response
type AccountDataResponse struct {
	Global []json.RawMessage            `json:"global"`
	Rooms  map[string][]json.RawMessage `json:"rooms"`
}

func (r *AccountDataResponse) HasData(isInitial bool) bool {
	if isInitial {
		return true
	}
	return len(r.Rooms) > 0
}

func ProcessAccountData(store *state.Storage, userID string, isInitial bool, req *AccountDataRequest) (res *AccountDataResponse) {
	res = &AccountDataResponse{}
	return
}
