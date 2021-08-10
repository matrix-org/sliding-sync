package streams

import (
	"errors"

	"github.com/matrix-org/sync-v3/sync3"
)

// Streamer specifies an interface which, if satisfied, can be used to make a stream.
type Streamer interface {
	// Return the position in the correct stream based on a sync v3 token
	Position(tok *sync3.Token) int64
	// Set the position of this stream in the token to the `pos` value given
	SetPosition(tok *sync3.Token, pos int64)
	// Extract data between the two stream positions and assign to Response.
	// Return the new to position if it has been modified, else zero.
	DataInRange(session *sync3.Session, fromExcl, toIncl int64, req *Request, resp *Response) (int64, error)
	// Called when a session hits /sync with a stream position. `allSessions` is true if all sessions
	// are at least as far as this position (inclusive), allowing cleanup of earlier messages.
	SessionConfirmed(session *sync3.Session, confirmedPos int64, allSessions bool)
	// Return true if this request contains pagination parameters for your stream. Use to detect
	// whether to block or not (pagination never blocks)
	IsPaginationRequest(req *Request) bool
}

// ErrNotRequested should be returned in DataInRange if the request does not ask for this stream.
var ErrNotRequested = errors.New("stream not requested")
