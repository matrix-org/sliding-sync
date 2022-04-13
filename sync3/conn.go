package sync3

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/matrix-org/sync-v3/internal"
)

type ConnID struct {
	DeviceID string
}

func (c *ConnID) String() string {
	return c.DeviceID
}

type ConnHandler interface {
	// Callback which is allowed to block as long as the context is active. Return the response
	// to send back or an error. Errors of type *internal.HandlerError are inspected for the correct
	// status code to send back.
	OnIncomingRequest(ctx context.Context, cid ConnID, req *Request, isInitial bool) (*Response, error)
	UserID() string
	Destroy()
	Alive() bool
}

// Conn is an abstraction of a long-poll connection. It automatically handles the position values
// of the /sync request, including sending cached data in the event of retries. It does not handle
// the contents of the data at all.
type Conn struct {
	ConnID ConnID

	handler ConnHandler

	// The position/data in the stream last sent by the client
	lastClientRequest Request

	// A buffer of the last response sent to the client.
	// Can be resent as-is if the server response was lost
	lastServerResponse Response

	// ensure only 1 incoming request is handled per connection
	mu                       *sync.Mutex
	cancelOutstandingRequest func()
}

func NewConn(connID ConnID, h ConnHandler) *Conn {
	return &Conn{
		ConnID:  connID,
		handler: h,
		mu:      &sync.Mutex{},
	}
}

func (c *Conn) Alive() bool {
	return c.handler.Alive()
}

func (c *Conn) tryRequest(ctx context.Context, req *Request) (res *Response, err error) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			err = fmt.Errorf("panic: %s", panicErr)
		}
	}()
	return c.handler.OnIncomingRequest(ctx, c.ConnID, req, req.pos == 0)
}

// OnIncomingRequest advances the clients position in the stream, returning the response position and data.
func (c *Conn) OnIncomingRequest(ctx context.Context, req *Request) (resp *Response, herr *internal.HandlerError) {
	if c.cancelOutstandingRequest != nil {
		c.cancelOutstandingRequest()
	}
	c.mu.Lock()
	ctx, cancel := context.WithCancel(ctx)
	c.cancelOutstandingRequest = cancel
	// it's intentional for the lock to be held whilst inside HandleIncomingRequest
	// as it guarantees linearisation of data within a single connection
	defer c.mu.Unlock()

	if req.pos != 0 && c.lastClientRequest.pos == req.pos {
		// if the request bodies match up then this is a retry, else it could be the client modifying
		// their filter params, so fallthrough
		if c.lastClientRequest.Same(req) {
			// this is the 2nd+ time we've seen this request, meaning the client likely retried this
			// request. Send the response we sent before.
			logger.Trace().Int64("pos", req.pos).Msg("returning cached response for pos")
			return &c.lastServerResponse, nil
		}
	}
	// if there is a position and it isn't something we've told the client nor a retransmit, they
	// are playing games
	if req.pos != 0 && req.pos != c.lastServerResponse.PosInt() && c.lastClientRequest.pos != req.pos {
		// the client made up a position, reject them
		logger.Trace().Int64("pos", req.pos).Msg("unknown pos")
		return nil, &internal.HandlerError{
			StatusCode: 400,
			Err:        fmt.Errorf("unknown position: %d", req.pos),
		}
	}

	resp, err := c.tryRequest(ctx, req)
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
	// assign the last client request now _after_ we have processed the request so we don't incorrectly
	// cache errors or panics and result in getting wedged or tightlooping.
	c.lastClientRequest = *req
	var posInt int
	if c.lastServerResponse.Pos != "" {
		posInt, err = strconv.Atoi(c.lastServerResponse.Pos)
		if err != nil {
			return nil, &internal.HandlerError{
				StatusCode: 500,
				Err:        err,
			}
		}
	}
	resp.Pos = fmt.Sprintf("%d", posInt+1)
	c.lastServerResponse = *resp

	return resp, nil
}
