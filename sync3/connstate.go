package sync3

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"time"
)

var (
	// The max number of events the client is eligible to read (unfiltered) which we are willing to
	// buffer on this connection. Too large and we consume lots of memory. Too small and busy accounts
	// will trip the connection knifing.
	MaxPendingEventUpdates = 200
)

type ConnStateStore interface {
	LoadRoom(roomID string) *SortableRoom
	LoadState(roomID string, loadPosition int64, requiredState [][2]string) []json.RawMessage
	Load(userID string) (joinedRoomIDs []string, initialLoadPosition int64, err error)
}

// ConnState tracks all high-level connection state for this connection, like the combined request
// and the underlying sorted room list. It doesn't track session IDs or positions of the connection.
type ConnState struct {
	store                      ConnStateStore
	muxedReq                   *Request
	userID                     string
	sortedJoinedRooms          SortableRooms
	sortedJoinedRoomsPositions map[string]int // room_id -> index in sortedJoinedRooms
	roomSubscriptions          map[string]RoomSubscription
	loadPosition               int64
	// A channel which v2 poll loops use to send updates to, via the ConnMap.
	// Consumed when the conn is read. There is a limit to how many updates we will store before
	// saying the client is ded and cleaning up the conn.
	updateEvents chan *EventData
}

func NewConnState(userID string, store ConnStateStore) *ConnState {
	return &ConnState{
		store:                      store,
		userID:                     userID,
		roomSubscriptions:          make(map[string]RoomSubscription),
		sortedJoinedRoomsPositions: make(map[string]int),
		updateEvents:               make(chan *EventData, MaxPendingEventUpdates), // TODO: customisable
	}
}

// load the initial joined room list, unfiltered and unsorted, and cache up the fields we care about
// like the room name. We have synchronisation issues here similar to the ConnMap's initial Load.
// However, unlike the ConnMap, we cannot just say "don't start any v2 poll loops yet". To keep things
// synchronised from duplicate event processing, this function will remember the latest NID it used
// to load the initial state, then ignore all incoming events until a syncPosition > the load position
// is received. This guards against the following race condition:
//   - Conn is made. It is atomically added to the ConnMap, making it immediately eligible to be pushed new events.
//   - Between the Conn being added to the ConnMap and the call to load() (done when we get the first HandleIncomingRequest call)
//     N events arrive and get buffered.
//   - load() bases its current state based on the latest position, which includes processing of these N events.
//   - post load() we read N events, processing them a 2nd time.
func (s *ConnState) load(req *Request) error {
	joinedRoomIDs, initialLoadPosition, err := s.store.Load(s.userID)
	if err != nil {
		return err
	}
	s.loadPosition = initialLoadPosition
	s.sortedJoinedRooms = make([]SortableRoom, len(joinedRoomIDs))
	for i, roomID := range joinedRoomIDs {
		// load global room info
		sr := s.store.LoadRoom(roomID)
		s.sortedJoinedRooms[i] = *sr
		s.sortedJoinedRoomsPositions[sr.RoomID] = i
	}
	s.sort(req.Sort)

	return nil
}

func (s *ConnState) sort(sortBy []string) {
	// TODO: read sortBy, for now we always sort by most recent timestamp
	sort.SliceStable(s.sortedJoinedRooms, func(i, j int) bool {
		return s.sortedJoinedRooms[i].LastMessageTimestamp > s.sortedJoinedRooms[j].LastMessageTimestamp
	})
	for i := range s.sortedJoinedRooms {
		s.sortedJoinedRoomsPositions[s.sortedJoinedRooms[i].RoomID] = i
	}
	//logger.Info().Interface("pos", c.sortedJoinedRoomsPositions).Msg("sorted")
}

func (s *ConnState) HandleIncomingRequest(ctx context.Context, cid ConnID, req *Request) (*Response, error) {
	if s.loadPosition == 0 {
		s.load(req)
	}
	return s.onIncomingRequest(ctx, req)
}

// PushNewEvent is a callback which fires when the server gets a new event and determines this connection MAY be
// interested in it (e.g the client is joined to the room or it's an invite, etc). Each callback can fire
// from different v2 poll loops, and there is no locking in order to prevent a slow ConnState from wedging the poll loop.
// We need to move this data onto a channel for onIncomingRequest to consume later.
func (s *ConnState) PushNewEvent(eventData *EventData) {
	// TODO: remove 0 check when Initialise state returns sensible positions
	if eventData.latestPos != 0 && eventData.latestPos < s.loadPosition {
		// do not push this event down the stream as we have already processed it when we loaded
		// the room list initially.
		return
	}
	select {
	case s.updateEvents <- eventData:
	case <-time.After(5 * time.Second):
		// TODO: kill the connection
		logger.Warn().Interface("event", *eventData).Str("user", s.userID).Msg(
			"cannot send event to connection, buffer exceeded",
		)
	}
}

// onIncomingRequest is a callback which fires when the client makes a request to the server. Whilst each request may
// be on their own goroutine, the requests are linearised for us by Conn so it is safe to modify ConnState without
// additional locking mechanisms.
func (s *ConnState) onIncomingRequest(ctx context.Context, req *Request) (*Response, error) {
	var prevRange SliceRanges
	var prevSort []string
	if s.muxedReq != nil {
		prevRange = s.muxedReq.Rooms
		prevSort = s.muxedReq.Sort
	}
	var newSubs []string
	var newUnsubs []string
	if s.muxedReq == nil {
		s.muxedReq = req
		for roomID := range req.RoomSubscriptions {
			newSubs = append(newSubs, roomID)
		}
	} else {
		combinedReq, subs, unsubs := s.muxedReq.ApplyDelta(req)
		s.muxedReq = combinedReq
		newSubs = subs
		newUnsubs = unsubs
	}

	// start forming the response
	response := &Response{
		RoomSubscriptions: s.updateRoomSubscriptions(newSubs, newUnsubs),
		Count:             int64(len(s.sortedJoinedRooms)),
	}

	// TODO: calculate the M values for N < M calcs

	var responseOperations []ResponseOp

	var added, removed, same SliceRanges
	if prevRange != nil {
		added, removed, same = prevRange.Delta(s.muxedReq.Rooms)
	} else {
		added = s.muxedReq.Rooms
	}

	if !reflect.DeepEqual(prevSort, s.muxedReq.Sort) {
		// the sort operations have changed, invalidate everything (if there were previous syncs), re-sort and re-SYNC
		if prevSort != nil {
			for _, r := range s.muxedReq.Rooms {
				responseOperations = append(responseOperations, &ResponseOpRange{
					Operation: "INVALIDATE",
					Range:     r[:],
				})
			}
		}
		s.sort(s.muxedReq.Sort)
		added = s.muxedReq.Rooms
		removed = nil
		same = nil
	}

	// send INVALIDATE for these ranges
	for _, r := range removed {
		responseOperations = append(responseOperations, &ResponseOpRange{
			Operation: "INVALIDATE",
			Range:     r[:],
		})
	}
	// send full room data for these ranges
	for _, r := range added {
		sr := SliceRanges([][2]int64{r})
		subslice := sr.SliceInto(s.sortedJoinedRooms)
		rooms := subslice[0].(SortableRooms)
		roomsResponse := make([]Room, len(rooms))
		for i := range rooms {
			roomData := s.getInitialRoomData(rooms[i].RoomID)
			roomsResponse[i] = *roomData
		}
		responseOperations = append(responseOperations, &ResponseOpRange{
			Operation: "SYNC",
			Range:     r[:],
			Rooms:     roomsResponse,
		})
	}
	// do live tracking if we haven't changed the range and we have nothing to tell the client yet
	if same != nil && len(responseOperations) == 0 && len(response.RoomSubscriptions) == 0 {
		// block until we get a new event, with appropriate timeout
	blockloop:
		for {
			select {
			case <-ctx.Done(): // client has given up
				break blockloop
			case <-time.After(10 * time.Second): // TODO configurable
				break blockloop
			case updateEvent := <-s.updateEvents:
				if updateEvent.latestPos > s.loadPosition {
					s.loadPosition = updateEvent.latestPos
				}
				// TODO: Add filters to check if this event should cause a response or should be dropped (e.g filtering out messages)
				// this is why this select is in a while loop as not all update event will wake up the stream

				// TODO: Implement sorting by something other than recency. With recency sorting,
				// most operations are DELETE/INSERT to bump rooms to the top of the list. We only
				// do an UPDATE if the most recent room gets a 2nd event.
				var targetRoom SortableRoom
				fromIndex, ok := s.sortedJoinedRoomsPositions[updateEvent.roomID]
				var lastTimestamp int64
				if !ok {
					// the user may have just joined the room hence not have an entry in this list yet.
					fromIndex = len(s.sortedJoinedRooms)
					newRoom := s.store.LoadRoom(updateEvent.roomID)
					newRoom.LastMessageTimestamp = updateEvent.timestamp
					s.sortedJoinedRooms = append(s.sortedJoinedRooms, *newRoom)
					targetRoom = *newRoom
				} else {
					targetRoom = s.sortedJoinedRooms[fromIndex]
					lastTimestamp = targetRoom.LastMessageTimestamp
					targetRoom.LastEventJSON = updateEvent.event
					targetRoom.LastMessageTimestamp = updateEvent.timestamp
					s.sortedJoinedRooms[fromIndex] = targetRoom
				}
				// re-sort
				s.sort(nil)

				if _, ok := s.roomSubscriptions[updateEvent.roomID]; ok {
					// there is a subscription for this room, so update the room subscription field
					// TODO: optimise by only sending the room ID delta for index positions
					response.RoomSubscriptions[updateEvent.roomID] = *s.getDeltaRoomData(updateEvent)
				}
				toIndex := s.sortedJoinedRoomsPositions[updateEvent.roomID]
				logger.Info().Int("from", fromIndex).Int("to", toIndex).
					Int64("prev_ts", lastTimestamp).Int64("event_ts", updateEvent.timestamp).
					Interface("room", targetRoom.RoomID).Msg("moved!")
				// the toIndex may not be inside a tracked range. If it isn't, we actually need to notify about a
				// different room
				if !s.muxedReq.Rooms.Inside(int64(toIndex)) {
					logger.Info().Msg("room isn't inside tracked range")
					toIndex = int(s.muxedReq.Rooms.UpperClamp(int64(toIndex)))
					if toIndex >= len(s.sortedJoinedRooms) {
						// no room exists
						logger.Warn().Int("to", toIndex).Int("size", len(s.sortedJoinedRooms)).Msg(
							"cannot move to index, it's greater than the list of sorted rooms",
						)
						continue
					}
					if toIndex == -1 {
						logger.Warn().Int("from", fromIndex).Int("to", toIndex).Interface("ranges", s.muxedReq.Rooms).Msg(
							"room moved but not in tracked ranges, ignoring",
						)
						continue
					}
					// TODO inject last event if never seen before, else just room ID updateEvent = s.sortedJoinedRooms[toIndex].LastEvent
					toRoom := s.sortedJoinedRooms[toIndex]
					// fake an update event for this room.
					// We do this because we are introducing a new room in the list because of this situation:
					// tracking [10,20] and room 24 jumps to position 0, so now we are tracking [9,19] as all rooms
					// have been shifted to the right
					updateEvent = &EventData{
						event:  toRoom.LastEventJSON,
						roomID: toRoom.RoomID,
					}
				}

				responseOperations = append(
					responseOperations, s.moveRoom(updateEvent, fromIndex, toIndex, s.muxedReq.Rooms)...,
				)
				break blockloop
			}
		}
	}

	response.Ops = responseOperations

	return response, nil
}

func (s *ConnState) updateRoomSubscriptions(subs, unsubs []string) map[string]Room {
	result := make(map[string]Room)
	for _, roomID := range subs {
		sub, ok := s.muxedReq.RoomSubscriptions[roomID]
		if !ok {
			logger.Warn().Str("room_id", roomID).Msg(
				"room listed in subscriptions but there is no subscription information in the request, ignoring room subscription.",
			)
			continue
		}
		s.roomSubscriptions[roomID] = sub
		// send initial room information
		room := s.getInitialRoomData(roomID)
		result[roomID] = *room
	}
	for _, roomID := range unsubs {
		delete(s.roomSubscriptions, roomID)
	}
	return result
}

func (s *ConnState) getDeltaRoomData(updateEvent *EventData) *Room {
	return &Room{
		RoomID: updateEvent.roomID,
		// TODO: notif counts
		Timeline: []json.RawMessage{
			updateEvent.event,
		},
	}
}

func (s *ConnState) getInitialRoomData(roomID string) *Room {
	r := s.store.LoadRoom(roomID)
	return &Room{
		RoomID: roomID,
		Name:   r.Name,
		// TODO: notif counts
		// TODO: timeline limits
		Timeline: []json.RawMessage{
			r.LastEventJSON,
		},
		RequiredState: s.store.LoadState(roomID, s.loadPosition, s.muxedReq.RequiredState),
	}
}

func (s *ConnState) UserID() string {
	return s.userID
}

// Move a room from an absolute index position to another absolute position.
// 1,2,3,4,5
// 3 bumps to top -> 3,1,2,4,5 -> DELETE index=2, INSERT val=3 index=0
// 7 bumps to top -> 7,1,2,3,4 -> DELETE index=4, INSERT val=7 index=0
func (s *ConnState) moveRoom(updateEvent *EventData, fromIndex, toIndex int, ranges SliceRanges) []ResponseOp {
	if fromIndex == toIndex {
		// issue an UPDATE, nice and easy because we don't need to move entries in the list
		return []ResponseOp{
			&ResponseOpSingle{
				Operation: "UPDATE",
				Index:     &fromIndex,
				Room:      s.getDeltaRoomData(updateEvent),
			},
		}
	}
	// work out which value to DELETE. This varies depending on where the room was and how much of the
	// list we are tracking. E.g moving to index=0 with ranges [0,99][100,199] and an update in
	// pos 150 -> DELETE 150, but if we weren't tracking [100,199] then we would DELETE 99. If we were
	// tracking [0,99][200,299] then it's still DELETE 99 as the 200-299 range isn't touched.
	deleteIndex := fromIndex
	if !ranges.Inside(int64(fromIndex)) {
		// we are not tracking this room, so no point issuing a DELETE for it. Instead, clamp the index
		// to the highest end-range marker < index
		deleteIndex = int(ranges.LowerClamp(int64(fromIndex)))
	}
	return []ResponseOp{
		&ResponseOpSingle{
			Operation: "DELETE",
			Index:     &deleteIndex,
		},
		&ResponseOpSingle{
			Operation: "INSERT",
			Index:     &toIndex,
			// TODO: check if we have sent this room before and if so, don't send all the data ever
			Room: s.getInitialRoomData(updateEvent.roomID),
		},
	}

}
