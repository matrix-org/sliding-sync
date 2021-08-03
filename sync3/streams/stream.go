package streams

import (
	"errors"

	"github.com/matrix-org/sync-v3/sync3"
)

// Streamer specifies an interface which, if satisfied, can be used to make a stream.
type Streamer interface {
	// Return the position in the correct stream based on a sync v3 token
	Position(tok *sync3.Token) int64
	// Set the stream position for this stream in the sync v3 token
	// SetPosition(tok *sync3.Token, pos int64)
	// Extract data between the two stream positions and assign to Response.
	DataInRange(session *sync3.Session, fromExcl, toIncl int64, req *Request, resp *Response) error
}

// ErrNotRequested should be returned in DataInRange if the request does not ask for this stream.
var ErrNotRequested = errors.New("stream not requested")
