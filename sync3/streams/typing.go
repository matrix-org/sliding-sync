package streams

import (
	"fmt"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
)

type FilterTyping struct {
	// The room ID to get typing notifications in.
	RoomID string `json:"room_id"`
}

type TypingResponse struct {
	// A list of user IDs of who is currently typing.
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

func (s *Typing) SetPosition(tok *sync3.Token, pos int64) {
	tok.SetTypingPosition(pos)
}

func (s *Typing) IsPaginationRequest(req *Request) bool {
	return false // no pagination support
}

func (s *Typing) SessionConfirmed(session *sync3.Session, confirmedPos int64, allSessions bool) {}

func (s *Typing) DataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) (int64, error) {
	if request.Typing == nil {
		return 0, ErrNotRequested
	}
	userIDs, _, err := s.storage.TypingTable.Typing(request.Typing.RoomID, fromExcl, toIncl)
	if err != nil {
		return 0, fmt.Errorf("Typing: %s", err)
	}
	resp.Typing = &TypingResponse{
		UserIDs: userIDs,
	}
	return 0, nil
}
