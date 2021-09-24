package synclive

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/matrix-org/sync-v3/state"
)

var (
	// The max number of events the client is eligible to read (unfiltered) which we are willing to
	// buffer on this connection. Too large and we consume lots of memory. Too small and busy accounts
	// will trip the connection knifing.
	MaxPendingEventUpdates = 100
)

// ConnState tracks all high-level connection state for this connection, like the combined request
// and the underlying sorted room list. It doesn't track session IDs or positions of the connection.
type ConnState struct {
	store                      *state.Storage
	muxedReq                   *Request
	userID                     string
	sortedJoinedRooms          SortableRooms
	sortedJoinedRoomsPositions map[string]int   // room_id -> index in sortedJoinedRooms
	roomSubscriptions          map[string]*Room // TODO
	initialLoadPosition        int64
	loadRoom                   func(roomID string) *SortableRoom
	// A channel which v2 poll loops use to send updates to, via the ConnMap.
	// Consumed when the conn is read. There is a limit to how many updates we will store before
	// saying the client is ded and cleaning up the conn.
	updateEvents chan *EventData
}

func NewConnState(userID string, store *state.Storage, loadRoom func(roomID string) *SortableRoom) *ConnState {
	return &ConnState{
		store:                      store,
		userID:                     userID,
		loadRoom:                   loadRoom,
		roomSubscriptions:          make(map[string]*Room),
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
func (c *ConnState) load() error {
	// load from store
	var err error
	c.initialLoadPosition, err = c.store.LatestEventNID()
	if err != nil {
		return err
	}
	joinedRoomIDs, err := c.store.JoinedRoomsAfterPosition(c.userID, c.initialLoadPosition)
	if err != nil {
		return err
	}
	c.sortedJoinedRooms = make([]SortableRoom, len(joinedRoomIDs))
	for i, roomID := range joinedRoomIDs {
		// load global room info
		sr := c.loadRoom(roomID)
		c.sortedJoinedRooms[i] = SortableRoom{
			RoomID: sr.RoomID,
			Name:   sr.Name,
		}
		c.sortedJoinedRoomsPositions[sr.RoomID] = i
	}
	return nil
}

func (c *ConnState) sort(sortBy []string) {
	logger.Info().Msg("sorting")
	// TODO: read sortBy, for now we always sort by most recent timestamp
	sort.SliceStable(c.sortedJoinedRooms, func(i, j int) bool {
		return c.sortedJoinedRooms[i].LastMessageTimestamp < c.sortedJoinedRooms[j].LastMessageTimestamp
	})
	for i := range c.sortedJoinedRooms {
		c.sortedJoinedRoomsPositions[c.sortedJoinedRooms[i].RoomID] = i
	}
}

func (c *ConnState) HandleIncomingRequest(ctx context.Context, conn *Conn, req *Request) (*Response, error) {
	if c.initialLoadPosition == 0 {
		c.load()
	}
	return c.onIncomingRequest(ctx, req)
}

// PushNewEvent is a callback which fires when the server gets a new event and determines this connection MAY be
// interested in it (e.g the client is joined to the room or it's an invite, etc). Each callback can fire
// from different v2 poll loops, and there is no locking in order to prevent a slow ConnState from wedging the poll loop.
// We need to move this data onto a channel for onIncomingRequest to consume later.
func (c *ConnState) PushNewEvent(eventData *EventData) {
	// TODO: remove 0 check when Initialise state returns sensible positions
	if eventData.latestPos != 0 && eventData.latestPos < c.initialLoadPosition {
		// do not push this event down the stream as we have already processed it when we loaded
		// the room list initially.
		return
	}
	select {
	case c.updateEvents <- eventData:
	case <-time.After(5 * time.Second):
		// TODO: kill the connection
		logger.Warn().Interface("event", *eventData).Str("user", c.userID).Msg(
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
	if s.muxedReq == nil {
		s.muxedReq = req
	} else {
		combinedReq, _, _ := s.muxedReq.ApplyDelta(req)
		s.muxedReq = combinedReq
	}

	// TODO: update room subscriptions
	// TODO: calculate the M values for N < M calcs
	fmt.Println("range", s.muxedReq.Rooms, "prev_range", prevRange, "sort", prevSort)

	var responseOperations []ResponseOp

	var added, removed, same SliceRanges
	if prevRange != nil {
		added, removed, same = prevRange.Delta(s.muxedReq.Rooms)
	} else {
		added = s.muxedReq.Rooms
	}

	if !reflect.DeepEqual(prevSort, s.muxedReq.Sort) {
		// the sort operations have changed, invalidate everything, re-sort and re-SYNC
		for _, r := range s.muxedReq.Rooms {
			responseOperations = append(responseOperations, &ResponseOpRange{
				Operation: "INVALIDATE",
				Range:     r[:],
			})
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
			roomsResponse[i] = Room{
				RoomID: rooms[i].RoomID,
				Name:   rooms[i].Name,
			}
		}
		responseOperations = append(responseOperations, &ResponseOpRange{
			Operation: "SYNC",
			Range:     r[:],
			Rooms:     roomsResponse,
		})
	}
	// do live tracking if we haven't changed the range and we have nothing to tell the client yet
	if same != nil && len(responseOperations) == 0 {
		// block until we get a new event, with appropriate timeout
	blockloop:
		for {
			select {
			case <-ctx.Done(): // client has given up
				break blockloop
			case <-time.After(10 * time.Second): // TODO configurable
				break blockloop
			case updateEvent := <-s.updateEvents:
				// TODO: Add filters to check if this event should cause a response or should be dropped (e.g filtering out messages)
				// TODO: Implement sorting by something other than recency. With recency sorting,
				// most operations are DELETE/INSERT to bump rooms to the top of the list. We only
				// do an UPDATE if the most recent room gets a 2nd event.
				fromIndex, ok := s.sortedJoinedRoomsPositions[updateEvent.roomID]
				if !ok {
					// the user may have just joined the room hence not have an entry in this list yet.
					fromIndex = -1
				}
				toIndex := 0 // TODO: this won't always be 0 if we sort by something other than recency

				// move the server's representation
				swap := s.sortedJoinedRooms[toIndex]
				var room *SortableRoom
				if fromIndex == -1 {
					room = s.loadRoom(updateEvent.roomID)
					// TODO: work out which index position this should be sorted into, depending on the sort operations
					// for now we always insert it into toIndex+1
					s.sortedJoinedRooms = append([]SortableRoom{
						s.sortedJoinedRooms[0], *room,
					}, s.sortedJoinedRooms[1:]...)
					fromIndex = 1
				} else {
					room = &s.sortedJoinedRooms[fromIndex]
				}
				s.sortedJoinedRooms[toIndex] = *room
				s.sortedJoinedRooms[fromIndex] = swap
				s.sortedJoinedRoomsPositions[room.RoomID] = toIndex
				s.sortedJoinedRoomsPositions[swap.RoomID] = fromIndex

				responseOperations = append(
					responseOperations, s.moveRoom(updateEvent, fromIndex, toIndex, s.muxedReq.Rooms)...,
				)
			}
		}
	}

	return &Response{
		Ops:   responseOperations,
		Count: int64(len(s.sortedJoinedRooms)),
	}, nil
}

func (s *ConnState) UserID() string {
	return s.userID
}

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
				Room: &Room{
					RoomID: updateEvent.roomID,
					Name:   s.sortedJoinedRooms[fromIndex].Name,
					Timeline: []json.RawMessage{
						updateEvent.event,
					},
				},
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
	room := s.loadRoom(updateEvent.roomID)
	return []ResponseOp{
		&ResponseOpSingle{
			Operation: "DELETE",
			Index:     &deleteIndex,
		},
		&ResponseOpSingle{
			Operation: "INSERT",
			Index:     &toIndex,
			// TODO: check if we have sent this room before and if so, don't send all the data ever
			Room: &Room{
				RoomID: room.RoomID,
				Name:   room.Name,
				Timeline: []json.RawMessage{
					updateEvent.event,
				},
			},
		},
	}

}
