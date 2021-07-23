package sync3

import "github.com/matrix-org/sync-v3/sync3/streams"

type Response struct {
	Next   string                  `json:"next"`
	Typing *streams.TypingResponse `json:"typing,omitempty"`
}
