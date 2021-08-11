package streams

import (
	"encoding/json"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
)

type RoomListSortOrder string

const (
	// Sort rooms based on the most recent event in the room. Newer first.
	SortRoomListByRecency RoomListSortOrder = "by_recent"
	// Sort rooms based on their tag in this user's account data.
	// Follows the `order` value if one exists. Lower first.
	// See https://matrix.org/docs/spec/client_server/latest#room-tagging
	SortRoomListByTag RoomListSortOrder = "by_tag"
)

// FilterRoomList represents a filter on the RoomList stream
type FilterRoomList struct {
	// Which event types should be returned as the latest event in the room.
	// Clients should include only events they know how to render here.
	// Empty set = everything
	LastEventTypes []string `json:"last_event_types"`
	// The number of rooms to return per request.
	// Clients should return at least 1 screen's worth of data (based on viewport size)
	// Server can override this value.
	Limit int `json:"limit"`
	// how to sort the rooms in the response. The first sort option is applied first, then any identical
	// values are sorted by the next sort option and so on. E.g [by_tag, by_recent] sorts the rooms by
	// tags first (abiding by the 'order' in the spec) then sorts matching tags by most recent first
	Sorts []RoomListSortOrder `json:"sorts"`
	// If true, return the m.room.name event if one exists
	// Low bandwidth clients may prefer just the `name` key on this event, and the ability to force
	// a truncation of the key if the name is too long, but this cannot be done in E2E rooms hence
	// this option is not presented in this stream.
	IncludeRoomName bool `json:"include_name"`
	// If true, return the m.room.avatar event if one exists
	IncludeRoomAvatar bool `json:"include_avatar"`
	// The pagination parameters to request the next page of results.
	P *P `json:"p,omitempty"`
}

type RoomListResponse struct {
	// Negotiated values
	Limit        int                 `json:"limit"`
	RoomNameSize int                 `json:"room_name_size"`
	Sorts        []RoomListSortOrder `json:"sorts"`
	// The rooms
	Rooms []RoomListEntry `json:"rooms"`
	// The pagination parameters to request the next page, can be empty if all rooms fit on one page.
	P *P `json:"p,omitempty"`
}

type RoomListEntry struct {
	RoomID      string          `json:"room_id"`
	NameEvent   json.RawMessage `json:"m.room.name,omitempty"`
	AvatarEvent json.RawMessage `json:"m.room.avatar,omitempty"`
	MemberCount int             `json:"member_count"`
	LastEvent   json.RawMessage `json:"last_event"`
	RoomType    string          `json:"room_type"` // e.g spaces
	IsDM        bool            `json:"dm"`        // from the m.direct event in account data
}

// RoomList represents a stream of room summaries.
// This stream is paginatable.
type RoomList struct {
	storage *state.Storage
}

func NewRoomList(s *state.Storage) *RoomList {
	return &RoomList{s}
}

func (s *RoomList) Position(tok *sync3.Token) int64 {
	return tok.EventPosition()
}

func (s *RoomList) SetPosition(tok *sync3.Token, pos int64) {
	tok.SetEventPosition(pos)
}

func (s *RoomList) IsPaginationRequest(req *Request) bool {
	return req.RoomList != nil && req.RoomList.P != nil && req.RoomList.P.Next != ""
}

func (s *RoomList) SessionConfirmed(session *sync3.Session, confirmedPos int64, allSessions bool) {
}

func (s *RoomList) DataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) (int64, error) {
	if request.RoomList == nil {
		return 0, ErrNotRequested
	}
	return 0, nil
}
