package synclive

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/matrix-org/sync-v3/internal"
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
	store               *state.Storage
	muxedReq            *Request
	userID              string
	sortedJoinedRooms   SortableRooms
	roomSubscriptions   map[string]*Room
	initialLoadPosition int64
	loadRoom            func(roomID string) *SortableRoom
	// A channel which v2 poll loops use to send updates to, via the ConnMap.
	// Consumed when the conn is read. There is a limit to how many updates we will store before
	// saying the client is ded and cleaning up the conn.
	updateEvents chan *EventData
}

func NewConnState(userID string, store *state.Storage, loadRoom func(roomID string) *SortableRoom) *ConnState {
	return &ConnState{
		store:             store,
		userID:            userID,
		loadRoom:          loadRoom,
		roomSubscriptions: make(map[string]*Room),
		updateEvents:      make(chan *EventData, MaxPendingEventUpdates), // TODO: customisable
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
	}
	return nil
}

func (c *ConnState) HandleIncomingRequest(ctx context.Context, conn *Conn, reqBody []byte) ([]byte, error) {
	if c.initialLoadPosition == 0 {
		c.load()
	}
	var req Request
	if err := json.Unmarshal(reqBody, &req); err != nil {
		return nil, &internal.HandlerError{
			StatusCode: 400,
			Err:        fmt.Errorf("failed to multiplex request data: %s", err),
		}
	}
	resp, err := c.onIncomingRequest(ctx, &req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(resp)
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
	if s.muxedReq != nil {
		prevRange = s.muxedReq.Rooms
	}
	if s.muxedReq == nil {
		s.muxedReq = req
	} else {
		combinedReq, _, _ := s.muxedReq.ApplyDelta(req)
		s.muxedReq = combinedReq
	}

	// TODO: update room subscriptions
	// TODO: calculate the M values for N < M calcs
	fmt.Println("range", s.muxedReq.Rooms, "prev_range", prevRange)

	var responseOperations []ResponseOp
	var added, removed, same SliceRanges
	if prevRange != nil {
		added, removed, same = prevRange.Delta(s.muxedReq.Rooms)
	} else {
		added = s.muxedReq.Rooms
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
				// does this event affect a range we're tracking? For now, always yes
				// If the event is for a room in the range -> UPDATE
				// If the event causes the room list to sort differently -> DELETE/INSERT
				// For now we treat the room list as 'by_recency' always so every event causes the
				// room list to re-sort.
				fmt.Printf("%+v\n", updateEvent)
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

// Load the current state of rooms from storage based on the request parameters
func LoadRooms(s *state.Storage, req *Request, roomIDs []string) (map[string]Room, error) {
	return nil, nil
}
