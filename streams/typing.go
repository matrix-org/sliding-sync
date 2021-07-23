package streams

import (
	"fmt"

	"github.com/matrix-org/sync-v3/state"
)

type FilterTyping struct {
	RoomID string `json:"room_id"`
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

func (s *Typing) Process(userID string, from, to int64, f *FilterTyping) (resp *TypingResponse, err error) {
	userIDs, err := s.storage.Typing(f.RoomID, from, to)
	if err != nil {
		return nil, fmt.Errorf("Typing: %s", err)
	}
	resp = &TypingResponse{
		UserIDs: userIDs,
	}
	return resp, nil
}
