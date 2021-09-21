package synclive

import (
	"bytes"
	"context"
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

// Conn is an abstraction of a long-poll connection. It automatically handles the position values
// of the /sync request, including sending cached data in the event of retries. It does not handle
// the contents of the data at all.
type Conn struct {
	ConnID ConnID
	// Callback which is allowed to block as long as the context is active. Return the response
	// to send back or an error. Errors of type *internal.HandlerError are inspected for the correct
	// status code to send back.
	HandleIncomingRequest func(ctx context.Context, connID ConnID, reqBody []byte) ([]byte, error)

	// The position/data in the stream last sent by the client
	lastClientRequest dataFrame

	// A buffer of the last response sent to the client.
	// Can be resent as-is if the server response was lost
	lastResponse dataFrame

	// ensure only 1 incoming request is handled per connection
	mu *sync.Mutex
}

type dataFrame struct {
	pos  int64 // The first position sent back is 1, so 0 means there was a problem.
	data []byte
}

func NewConn(connID ConnID) *Conn {
	return &Conn{
		ConnID: connID,
		mu:     &sync.Mutex{},
	}
}

// OnIncomingRequest advances the clients position in the stream, returning the response position and data.
func (c *Conn) OnIncomingRequest(ctx context.Context, pos int64, data []byte) (nextPos int64, nextData []byte, herr *internal.HandlerError) {
	c.mu.Lock()
	// it's intentional for the lock to be held whilst inside HandleIncomingRequest
	// as it guarantees linearisation of data within a single connection
	defer c.mu.Unlock()

	if pos != 0 && c.lastClientRequest.pos == pos {
		// if the request bodies match up then this is a retry, else it could be the client modifying
		// their filter params, so fallthrough
		if bytes.Equal(data, c.lastClientRequest.data) {
			// this is the 2nd+ time we've seen this request, meaning the client likely retried this
			// request. Send the response we sent before.
			return c.lastResponse.pos, c.lastResponse.data, nil
		}
	}
	c.lastClientRequest.pos = pos
	// notify handler as it may need to recalcualte or invalidate stuff
	responseBytes, err := c.HandleIncomingRequest(ctx, c.ConnID, data)
	if err != nil {
		herr, ok := err.(*internal.HandlerError)
		if !ok {
			herr = &internal.HandlerError{
				StatusCode: 500,
				Err:        err,
			}
		}
		responseBytes = herr.JSON()
	}
	c.lastResponse.pos += 1
	c.lastResponse.data = responseBytes

	return c.lastResponse.pos, c.lastResponse.data, nil

}
