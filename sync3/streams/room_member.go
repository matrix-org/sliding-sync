package streams

import (
	"encoding/json"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
)

const (
	defaultRoomMemberLimit = 50
	maxRoomMemberLimit     = 1000
)

type RoomMemberSortOrder string

var (
	SortRoomMemberByPL   RoomMemberSortOrder = "by_pl"
	SortRoomMemberByName RoomMemberSortOrder = "by_name"
)

type FilterRoomMember struct {
	Limit  int64               `json:"limit"`
	SortBy RoomMemberSortOrder `json:"sort_by"`
	P      P                   `json:"p"`
}

type RoomMemberResponse struct {
	Limit  int64             `json:"limit"`
	Events []json.RawMessage `json:"events"`
}

// RoomMember represents a stream of room members.
type RoomMember struct {
	storage *state.Storage
}

func NewRoomMember(s *state.Storage) *RoomMember {
	return &RoomMember{s}
}

func (s *RoomMember) Position(tok *sync3.Token) int64 {
	return tok.RoomMemberPosition()
}

func (s *RoomMember) SetPosition(tok *sync3.Token, pos int64) {
	tok.SetRoomMemberPosition(pos)
}

func (s *RoomMember) SessionConfirmed(session *sync3.Session, confirmedPos int64, allSessions bool) {
}

func (s *RoomMember) DataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) (int64, error) {
	if request.RoomMember == nil {
		return 0, ErrNotRequested
	}
	return toIncl, nil
}
