package sync3

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3/caches"
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
	OnUpdate(ctx context.Context, update caches.Update)
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

	// A buffer of the last responses sent to the client.
	// Can be resent as-is if the server response was lost.
	// This array has 3 categories: ACKed messages, the ACKed message, unACKed messages e.g
	// [ACK, ACK, ACKing, NACK, NACK]
	// - The ACKing message is always the response with the same pos as req.pos
	// - Everything before it is old and can be deleted
	// - Everything after that is new and unseen, and the first element is the one we want to return.
	serverResponses []Response
	lastPos         int64

	// ensure only 1 incoming request is handled per connection
	mu                         *sync.Mutex
	cancelOutstandingRequest   func()
	cancelOutstandingRequestMu *sync.Mutex
}

func NewConn(connID ConnID, h ConnHandler) *Conn {
	return &Conn{
		ConnID:                     connID,
		handler:                    h,
		mu:                         &sync.Mutex{},
		cancelOutstandingRequestMu: &sync.Mutex{},
	}
}

func (c *Conn) UserID() string {
	return c.handler.UserID()
}

func (c *Conn) Alive() bool {
	return c.handler.Alive()
}

func (c *Conn) OnUpdate(ctx context.Context, update caches.Update) {
	c.handler.OnUpdate(ctx, update)
}

func (c *Conn) tryRequest(ctx context.Context, req *Request) (res *Response, err error) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			err = fmt.Errorf("panic: %s", panicErr)
			logger.Error().Msg(string(debug.Stack()))
		}
	}()
	taskType := "OnIncomingRequest"
	if req.pos == 0 {
		taskType = "OnIncomingRequestInitial"
	}
	ctx, task := internal.StartTask(ctx, taskType)
	defer task.End()
	internal.Logf(ctx, "connstate", "starting user=%v device=%v pos=%v", c.handler.UserID(), c.ConnID.DeviceID, req.pos)
	return c.handler.OnIncomingRequest(ctx, c.ConnID, req, req.pos == 0)
}

func (c *Conn) isOutstanding(pos int64) bool {
	for _, r := range c.serverResponses {
		if r.PosInt() == pos {
			return true
		}
	}
	return false
}

// OnIncomingRequest advances the clients position in the stream, returning the response position and data.
func (c *Conn) OnIncomingRequest(ctx context.Context, req *Request) (resp *Response, herr *internal.HandlerError) {
	c.cancelOutstandingRequestMu.Lock()
	if c.cancelOutstandingRequest != nil {
		c.cancelOutstandingRequest()
	}
	c.cancelOutstandingRequestMu.Unlock()
	c.mu.Lock()
	ctx, cancel := context.WithCancel(ctx)

	c.cancelOutstandingRequestMu.Lock()
	c.cancelOutstandingRequest = cancel
	c.cancelOutstandingRequestMu.Unlock()
	// it's intentional for the lock to be held whilst inside HandleIncomingRequest
	// as it guarantees linearisation of data within a single connection
	defer c.mu.Unlock()

	isFirstRequest := req.pos == 0
	isRetransmit := !isFirstRequest && c.lastClientRequest.pos == req.pos
	isSameRequest := !isFirstRequest && c.lastClientRequest.Same(req)

	// if there is a position and it isn't something we've told the client nor a retransmit, they
	// are playing games
	if !isFirstRequest && !isRetransmit && !c.isOutstanding(req.pos) {
		// the client made up a position, reject them
		logger.Trace().Int64("pos", req.pos).Msg("unknown pos")
		return nil, internal.ExpiredSessionError()
	}

	// purge the response buffer based on the client's new position. Higher pos values are later.
	var nextUnACKedResponse *Response
	delIndex := -1
	for i := range c.serverResponses { // sorted so low pos are first
		if req.pos > c.serverResponses[i].PosInt() {
			// the client has advanced _beyond_ this position so it is safe to delete it, we won't
			// see it again as a retransmit
			delIndex = i
		} else if req.pos < c.serverResponses[i].PosInt() {
			// the client has not seen this response before, so we'll send it to them next no matter what.
			nextUnACKedResponse = &c.serverResponses[i]
			break
		}
	}
	c.serverResponses = c.serverResponses[delIndex+1:] // slice out the first delIndex+1 elements

	defer func() {
		l := logger.Trace().Int("num_res_acks", delIndex+1).Bool("is_retransmit", isRetransmit).Bool("is_first", isFirstRequest).Bool("is_same", isSameRequest).Int64("pos", req.pos).Str("user", c.handler.UserID())
		if nextUnACKedResponse != nil {
			l.Int64("new_pos", nextUnACKedResponse.PosInt())
		}

		l.Msg("OnIncomingRequest finished")
	}()

	if !isFirstRequest {
		if isRetransmit {
			// if the request bodies match up then this is a retry, else it could be the client modifying
			// their filter params, so fallthrough
			if isSameRequest {
				// this is the 2nd+ time we've seen this request, meaning the client likely retried this
				// request. Send the response we sent before.
				logger.Trace().Int64("pos", req.pos).Msg("returning cached response for pos")
				return nextUnACKedResponse, nil
			} else {
				logger.Info().Int64("pos", req.pos).Msg("client has resent this pos with different request data")
				// we need to fallthrough to process this request as the client will not resend this request data,
			}
		}
	}

	// if the client has no new data for us but we still have buffered responses, return that rather than
	// invoking the handler.
	if nextUnACKedResponse != nil {
		if isSameRequest {
			return nextUnACKedResponse, nil
		}
		// we have buffered responses but we cannot return it else we'll ignore the data in this request,
		// so we need to wait for this incoming request to be processed _before_ we can return the data.
		// To ensure this doesn't take too long, be cheeky and inject a low timeout value to ensure we
		// don't needlessly block.
		req.SetTimeoutMSecs(1)
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
	// this position is the highest stored pos +1
	resp.Pos = fmt.Sprintf("%d", c.lastPos+1)
	resp.TxnID = req.TxnID
	// buffer it
	c.serverResponses = append(c.serverResponses, *resp)
	c.lastPos = resp.PosInt()
	if nextUnACKedResponse == nil {
		nextUnACKedResponse = resp
	}

	// return the oldest value
	return nextUnACKedResponse, nil
}
