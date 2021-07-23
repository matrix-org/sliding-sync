package streams

import (
	"fmt"

	"github.com/matrix-org/sync-v3/state"
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

func (s *Typing) Process(userID string, from int64, f *FilterTyping) (resp *TypingResponse, next int64, err error) {
	userIDs, to, err := s.storage.Typing(f.RoomID, from)
	if err != nil {
		return nil, 0, fmt.Errorf("Typing: %s", err)
	}
	resp = &TypingResponse{
		UserIDs: userIDs,
	}
	return resp, to, nil
}
