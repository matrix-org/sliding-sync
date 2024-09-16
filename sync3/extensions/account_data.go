package extensions

import (
	"context"
	"encoding/json"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/rs/zerolog/log"
)

// Client created request params
type AccountDataRequest struct {
	Core
}

func (r *AccountDataRequest) Name() string {
	return "AccountDataRequest"
}

// Server response
type AccountDataResponse struct {
	Global []json.RawMessage            `json:"global,omitempty"`
	Rooms  map[string][]json.RawMessage `json:"rooms,omitempty"`
	// which rooms have had account data loaded from the DB in this response
	loadedRooms map[string]bool
}

func (r *AccountDataResponse) HasData(isInitial bool) bool {
	if isInitial {
		return true
	}
	return len(r.Rooms) > 0 || len(r.Global) > 0
}

func accountEventsAsJSON(events []state.AccountData) []json.RawMessage {
	j := make([]json.RawMessage, len(events))
	for i := range events {
		j[i] = events[i].Data
	}
	return j
}

func (r *AccountDataRequest) AppendLive(ctx context.Context, res *Response, extCtx Context, up caches.Update) {
	var globalMsgs []json.RawMessage
	roomToMsgs := map[string][]json.RawMessage{}
	switch update := up.(type) {
	case *caches.AccountDataUpdate:
		globalMsgs = accountEventsAsJSON(update.AccountData)
	case *caches.RoomAccountDataUpdate:
		if r.RoomInScope(update.RoomID(), extCtx) {
			roomToMsgs[update.RoomID()] = accountEventsAsJSON(update.AccountData)
		}
	case caches.RoomUpdate:
		if !r.RoomInScope(update.RoomID(), extCtx) {
			return
		}
		// if this is a room update which is included in the response, send account data for this room
		if _, exists := extCtx.RoomIDToTimeline[update.RoomID()]; exists {
			// ..but only if we haven't before
			if res.AccountData != nil && res.AccountData.loadedRooms[update.RoomID()] {
				// we've loaded this room before, don't do it again
				// this can happen when we consume lots of items in the buffer. If many of them are room updates
				// for the same room, we could send dupe room account data if we didn't do this check.
				return
			}
			roomAccountData, err := extCtx.Store.AccountDatas(extCtx.UserID, update.RoomID())
			if err != nil {
				log.Err(err).Str("user", extCtx.UserID).Str("room", update.RoomID()).Msg("failed to fetch room account data")
				internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
			} else {
				if len(roomAccountData) > 0 { // else we can end up with `null` not `[]`
					roomToMsgs[update.RoomID()] = accountEventsAsJSON(roomAccountData)
				}
			}
		}
	}
	if len(globalMsgs) == 0 && len(roomToMsgs) == 0 {
		return
	}
	if res.AccountData == nil {
		res.AccountData = &AccountDataResponse{
			Rooms:       make(map[string][]json.RawMessage),
			loadedRooms: make(map[string]bool),
		}
	}
	res.AccountData.Global = append(res.AccountData.Global, globalMsgs...)
	for roomID, roomAccountData := range roomToMsgs {
		res.AccountData.Rooms[roomID] = append(res.AccountData.Rooms[roomID], roomAccountData...)
		res.AccountData.loadedRooms[roomID] = true
	}
}

func (r *AccountDataRequest) ProcessInitial(ctx context.Context, res *Response, extCtx Context) {
	roomIDs := make([]string, len(extCtx.RoomIDToTimeline))
	i := 0
	for roomID := range extCtx.RoomIDToTimeline {
		if r.RoomInScope(roomID, extCtx) {
			roomIDs[i] = roomID
			i++
		}
	}
	extRes := &AccountDataResponse{
		Rooms:       make(map[string][]json.RawMessage),
		loadedRooms: make(map[string]bool),
	}
	// room account data needs to be sent every time the user scrolls the list to get new room IDs
	// TODO: remember which rooms the client has been told about
	if len(roomIDs) > 0 {
		roomsAccountData, err := extCtx.Store.AccountDatas(extCtx.UserID, roomIDs...)
		if err != nil {
			log.Err(err).Str("user", extCtx.UserID).Strs("rooms", roomIDs).Msg("failed to fetch room account data")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		} else {
			extRes.Rooms = make(map[string][]json.RawMessage)
			for _, ad := range roomsAccountData {
				extRes.Rooms[ad.RoomID] = append(extRes.Rooms[ad.RoomID], ad.Data)
				extRes.loadedRooms[ad.RoomID] = true
			}
		}
	}
	// global account data is only sent on the first connection, then we live stream
	if extCtx.IsInitial {
		globalAccountData, err := extCtx.Store.AccountDatas(extCtx.UserID)
		if err != nil {
			log.Err(err).Str("user", extCtx.UserID).Msg("failed to fetch global account data")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		} else {
			extRes.Global = accountEventsAsJSON(globalAccountData)
		}
	}
	if len(extRes.Rooms) > 0 || len(extRes.Global) > 0 {
		res.AccountData = extRes
	}
}
