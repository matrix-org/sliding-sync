package sync3

import (
	"context"
	"fmt"
	"sync"

	"github.com/matrix-org/sync-v3/internal"
)

type ConnID struct {
	SessionID string
	DeviceID  string
}

func (c *ConnID) String() string {
	return c.SessionID + "-" + c.DeviceID
}

type HandlerIncomingReqFunc func(ctx context.Context, cid ConnID, req *Request) (*Response, error)

// Conn is an abstraction of a long-poll connection. It automatically handles the position values
// of the /sync request, including sending cached data in the event of retries. It does not handle
// the contents of the data at all.
type Conn struct {
	ConnID ConnID
	// Callback which is allowed to block as long as the context is active. Return the response
	// to send back or an error. Errors of type *internal.HandlerError are inspected for the correct
	// status code to send back.
	HandleIncomingRequest HandlerIncomingReqFunc

	// The position/data in the stream last sent by the client
	lastClientRequest Request

	// A buffer of the last response sent to the client.
	// Can be resent as-is if the server response was lost
	lastServerResponse Response

	// ensure only 1 incoming request is handled per connection
	mu *sync.Mutex

	connState *ConnState
}

func NewConn(connID ConnID, connState *ConnState, fn HandlerIncomingReqFunc) *Conn {
	return &Conn{
		ConnID:                connID,
		HandleIncomingRequest: fn,
		mu:                    &sync.Mutex{},
		connState:             connState,
	}
}

func (c *Conn) PushNewEvent(eventData *EventData) {
	c.connState.PushNewEvent(eventData)
}

func (c *Conn) PushUserRoomData(userID, roomID string, data userRoomData, timestamp int64) {
	c.connState.PushNewEvent(&EventData{
		roomID:       roomID,
		userRoomData: &data,
		timestamp:    timestamp,
	})
}

// OnIncomingRequest advances the clients position in the stream, returning the response position and data.
func (c *Conn) OnIncomingRequest(ctx context.Context, req *Request) (resp *Response, herr *internal.HandlerError) {
	c.mu.Lock()
	// it's intentional for the lock to be held whilst inside HandleIncomingRequest
	// as it guarantees linearisation of data within a single connection
	defer c.mu.Unlock()

	if req.pos != 0 && c.lastClientRequest.pos == req.pos {
		// if the request bodies match up then this is a retry, else it could be the client modifying
		// their filter params, so fallthrough
		if c.lastClientRequest.Same(req) {
			// this is the 2nd+ time we've seen this request, meaning the client likely retried this
			// request. Send the response we sent before.
			return &c.lastServerResponse, nil
		}
	}
	// if there is a position and it isn't something we've told the client nor a retransmit, they
	// are playing games
	if req.pos != 0 && req.pos != c.lastServerResponse.Pos && c.lastClientRequest.pos != req.pos {
		// the client made up a position, reject them
		return nil, &internal.HandlerError{
			StatusCode: 400,
			Err:        fmt.Errorf("unknown position: %d", req.pos),
		}
	}
	c.lastClientRequest = *req

	resp, err := c.HandleIncomingRequest(ctx, c.ConnID, req)
	if err != nil {
		herr, ok := err.(*internal.HandlerError)
		if !ok {
			herr = &internal.HandlerError{
				StatusCode: 500,
				Err:        err,
			}
		}
		return nil, herr
	}
	resp.Pos = c.lastServerResponse.Pos + 1
	c.lastServerResponse = *resp

	return resp, nil
}
