package synclive

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/state"
)

// ConnState tracks all high-level connection state for this connection, like the combined request
// and the underlying sorted room list. It doesn't track session IDs or positions of the connection.
type ConnState struct {
	store             *state.Storage
	muxedReq          *Request
	userID            string
	sortedJoinedRooms []*Room
	roomSubscriptions map[string]*Room
	//updateOps chan
	updateEvents chan *EventData
}

func NewConnState(userID string, store *state.Storage) *ConnState {
	return &ConnState{
		store:             store,
		userID:            userID,
		roomSubscriptions: make(map[string]*Room),
		updateEvents:      make(chan *EventData, 100), // TODO: customisable
	}
}

func (c *ConnState) HandleIncomingRequest(ctx context.Context, conn *Conn, reqBody []byte) ([]byte, error) {
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
		responseOperations = append(responseOperations, &ResponseOpRange{
			Operation: "SYNC",
			Range:     r[:],
			Rooms:     nil, // TODO
		})
	}
	// continue tracking these rooms
	for _, r := range same {
		// block until there is a change in this range: UPDATE or DELETE/INSERT
		fmt.Println(r)
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
