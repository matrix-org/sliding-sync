package sync3

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

// The amount of time to artificially wait if the server detects spamming clients. This time will
// be added to responses when the server detects the same request being sent over and over e.g
// /sync?pos=5 then /sync?pos=5 over and over. Likewise /sync without a ?pos=.
var SpamProtectionInterval = 10 * time.Millisecond

type ConnID struct {
	UserID   string
	DeviceID string
	CID      string // client-supplied conn_id
}

func (c *ConnID) String() string {
	return fmt.Sprintf("%s|%s|%s", c.UserID, c.DeviceID, c.CID)
}

type ConnHandler interface {
	// Callback which is allowed to block as long as the context is active. Return the response
	// to send back or an error. Errors of type *internal.HandlerError are inspected for the correct
	// status code to send back.
	OnIncomingRequest(ctx context.Context, cid ConnID, req *Request, isInitial bool, start time.Time) (*Response, error)
	OnUpdate(ctx context.Context, update caches.Update)
	PublishEventsUpTo(roomID string, nid int64)
	Destroy()
	Alive() bool
	SetCancelCallback(cancel context.CancelFunc)
}

// Conn is an abstraction of a long-poll connection. It automatically handles the position values
// of the /sync request, including sending cached data in the event of retries. It does not handle
// the contents of the data at all.
type Conn struct {
	ConnID

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

func (c *Conn) Alive() bool {
	return c.handler.Alive()
}

func (c *Conn) OnUpdate(ctx context.Context, update caches.Update) {
	c.handler.OnUpdate(ctx, update)
}

// tryRequest is a wrapper around ConnHandler.OnIncomingRequest which automatically
// starts and closes a tracing task.
//
// If the wrapped call panics, it is recovered from, reported to Sentry, and an error
// is passed to the caller. If the wrapped call returns an error, that error is passed
// upwards but will NOT be logged to Sentry (neither here nor by the caller). Errors
// should be reported to Sentry as close as possible to the point of creating the error,
// to provide the best possible Sentry traceback.
func (c *Conn) tryRequest(ctx context.Context, req *Request, start time.Time) (res *Response, err error) {
	// TODO: include useful information from the request in the sentry hub/context
	// Might be better done in the caller though?
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			err = fmt.Errorf("panic: %s", panicErr)
			logger.Error().Msg(string(debug.Stack()))
			// Note: as we've captured the panicErr ourselves, there isn't much
			// difference between RecoverWithContext and CaptureException. But
			// there /is/ a small difference:
			//
			// - RecoverWithContext will generate generate an Sentry event marked with
			//   a "RecoveredException" hint
			// - CaptureException instead uses an "OriginalException" hint.
			//
			// I'm guessing that Sentry will use the former to display panicErr as
			// having come from a panic.
			internal.GetSentryHubFromContextOrDefault(ctx).RecoverWithContext(ctx, panicErr)
		}
	}()
	taskType := "OnIncomingRequest"
	if req.pos == 0 {
		taskType = "OnIncomingRequestInitial"
	}
	ctx, task := internal.StartTask(ctx, taskType)
	defer task.End()
	internal.Logf(ctx, "connstate", "starting user=%v device=%v pos=%v", c.UserID, c.ConnID.DeviceID, req.pos)
	return c.handler.OnIncomingRequest(ctx, c.ConnID, req, req.pos == 0, start)
}

func (c *Conn) isOutstanding(pos int64) bool {
	for _, r := range c.serverResponses {
		if r.PosInt() == pos {
			return true
		}
	}
	return false
}

// OnIncomingRequest advances the client's position in the stream, returning the response position and data.
// If an error is returned, it will be logged by the caller and transmitted to the
// client. It will NOT be reported to Sentry---this should happen as close as possible
// to the creation of the error (or else Sentry cannot provide a meaningful traceback.)
func (c *Conn) OnIncomingRequest(ctx context.Context, req *Request, start time.Time) (resp *Response, herr *internal.HandlerError) {
	ctx, span := internal.StartSpan(ctx, "OnIncomingRequest.AcquireMutex")
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
	span.End()

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
		l := logger.Trace().Int("num_res_acks", delIndex+1).Bool("is_retransmit", isRetransmit).Bool("is_first", isFirstRequest).Bool("is_same", isSameRequest).Int64("pos", req.pos).Str("user", c.UserID)
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
				logger.Trace().Int64("pos", req.pos).Msg("returning cached response for pos, with delay")
				// apply a small artificial wait to protect the proxy in case this is caused by a buggy
				// client sending the same request over and over
				time.Sleep(SpamProtectionInterval)
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

	resp, err := c.tryRequest(ctx, req, start)
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

func (c *Conn) SetCancelCallback(cancel context.CancelFunc) {
	c.handler.SetCancelCallback(cancel)
}
