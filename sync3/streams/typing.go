package streams

import (
	"fmt"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
)

type FilterTyping struct {
	RoomID string `json:"room_id"`
}

func (f *FilterTyping) Combine(other *FilterTyping) *FilterTyping {
	combined := &FilterTyping{
		RoomID: f.RoomID,
	}
	if other.RoomID != "" {
		combined.RoomID = other.RoomID
	}
	return combined
}

type TypingResponse struct {
	UserIDs []string `json:"user_ids"`
}

// Typing represents a stream of users who are typing.
type Typing struct {
	storage *state.Storage
}

func NewTyping(s *state.Storage) *Typing {
	return &Typing{s}
}

func (s *Typing) Position(tok *sync3.Token) int64 {
	return tok.TypingPosition()
}

func (s *Typing) DataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) error {
	if request.Typing == nil {
		return ErrNotRequested
	}
	userIDs, _, err := s.storage.TypingTable.Typing(request.Typing.RoomID, fromExcl, toIncl)
	if err != nil {
		return fmt.Errorf("Typing: %s", err)
	}
	resp.Typing = &TypingResponse{
		UserIDs: userIDs,
	}
	return nil
}
