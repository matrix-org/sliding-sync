package extensions

import (
	"encoding/json"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3/caches"
)

// Client created request params
type AccountDataRequest struct {
	Enabled bool `json:"enabled"`
}

func (r AccountDataRequest) ApplyDelta(next *AccountDataRequest) *AccountDataRequest {
	r.Enabled = next.Enabled
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

func accountEventsAsJSON(events []state.AccountData) []json.RawMessage {
	j := make([]json.RawMessage, len(events))
	for i := range events {
		j[i] = events[i].Data
	}
	return j
}

func ProcessLiveAccountData(up caches.Update, store *state.Storage, updateWillReturnResponse bool, userID string, req *AccountDataRequest) (res *AccountDataResponse) {
	switch update := up.(type) {
	case *caches.AccountDataUpdate:
		return &AccountDataResponse{
			Global: accountEventsAsJSON(update.AccountData),
		}
	case *caches.RoomAccountDataUpdate:
		return &AccountDataResponse{
			Rooms: map[string][]json.RawMessage{
				update.RoomID(): accountEventsAsJSON(update.AccountData),
			},
		}
	case caches.RoomUpdate:
		// this is a room update which is causing us to return, meaning we are interested in this room.
		// send account data for this room.
		if updateWillReturnResponse {
			roomAccountData, err := store.AccountDatas(userID, update.RoomID())
			if err != nil {
				logger.Err(err).Str("user", userID).Str("room", update.RoomID()).Msg("failed to fetch room account data")
			} else {
				return &AccountDataResponse{
					Rooms: map[string][]json.RawMessage{
						update.RoomID(): accountEventsAsJSON(roomAccountData),
					},
				}
			}
		}
	}
	return nil
}

func ProcessAccountData(store *state.Storage, listRoomIDs map[string]struct{}, userID string, isInitial bool, req *AccountDataRequest) (res *AccountDataResponse) {
	roomIDs := make([]string, len(listRoomIDs))
	i := 0
	for roomID := range listRoomIDs {
		roomIDs[i] = roomID
		i++
	}
	res = &AccountDataResponse{}
	// room account data needs to be sent every time the user scrolls the list to get new room IDs
	// TODO: remember which rooms the client has been told about
	if len(roomIDs) > 0 {
		roomsAccountData, err := store.AccountDatas(userID, roomIDs...)
		if err != nil {
			logger.Err(err).Str("user", userID).Strs("rooms", roomIDs).Msg("failed to fetch room account data")
		} else {
			res.Rooms = make(map[string][]json.RawMessage)
			for _, ad := range roomsAccountData {
				res.Rooms[ad.RoomID] = append(res.Rooms[ad.RoomID], ad.Data)
			}
		}
	}
	// global account data is only sent on the first connection, then we live stream
	if isInitial {
		globalAccountData, err := store.AccountDatas(userID)
		if err != nil {
			logger.Err(err).Str("user", userID).Msg("failed to fetch global account data")
		} else {
			res.Global = accountEventsAsJSON(globalAccountData)
		}
	}
	return
}
