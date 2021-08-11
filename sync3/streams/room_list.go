package streams

import (
	"encoding/json"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
)

const (
	MaxRoomList     = 1000
	DefaultRoomList = 100
)

var (
	DefaultRoomListSorts = []RoomListSortOrder{SortRoomListByTag, SortRoomListByRecency}
	KnownRoomListSorts   = map[RoomListSortOrder]bool{
		SortRoomListByTag:     true,
		SortRoomListByRecency: true,
	}
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
	// The event type, state key tuple of a piece of room state to return.
	IncludeStateEvents [][2]string `json:"include_state_events"`
	// The pagination parameters to request the next page of results.
	P *P `json:"p,omitempty"`
}

func (r *FilterRoomList) Validate() {
	if r.Limit > MaxRoomList {
		r.Limit = MaxRoomList
	}
	if r.Limit <= 0 {
		r.Limit = DefaultRoomList
	}
	if r.Sorts == nil {
		r.Sorts = DefaultRoomListSorts
	}
	// validate the sorts
	for i := range r.Sorts {
		if !KnownRoomListSorts[r.Sorts[i]] {
			// remove it
			r.Sorts = append(r.Sorts[:i], r.Sorts[i+1:]...)
		}
	}
}

type RoomListResponse struct {
	// Negotiated values
	Limit int                 `json:"limit"`
	Sorts []RoomListSortOrder `json:"sorts"`
	// The rooms
	Rooms []RoomListEntry `json:"rooms"`
	// The pagination parameters to request the next page, can be empty if all rooms fit on one page.
	P *P `json:"p,omitempty"`
}

type RoomListEntry struct {
	RoomID      string                     `json:"room_id"`
	StateEvents map[string]json.RawMessage `json:"state_events"`
	MemberCount int                        `json:"member_count"`
	LastEvent   json.RawMessage            `json:"last_event"`
	RoomType    string                     `json:"room_type"` // e.g spaces
	IsDM        bool                       `json:"dm"`        // from the m.direct event in account data
	NumUnread   int                        `json:"unread"`
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
	request.RoomList.Validate()
	if request.RoomList.P == nil && fromExcl != 0 {
		return s.streamingDataInRange(session, fromExcl, toIncl, request, resp)
	}

	// flesh out the response - if we have been given a position then use it, else default to the latest position (for first syncs)
	paginationPos := fromExcl
	if paginationPos == 0 {
		paginationPos = toIncl
	}
	err := s.paginatedDataAtPoint(session, paginationPos, request, resp)
	if err != nil {
		return 0, err
	}

	// pagination never advances the token
	return fromExcl, nil
}

func (s *RoomList) paginatedDataAtPoint(session *sync3.Session, pos int64, request *Request, resp *Response) error {
	// find all invited / joined rooms for this user
	// populate summaries
	// offset based on P
	return nil
}

func (s *RoomList) streamingDataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) (int64, error) {
	// TODO
	return toIncl, nil
}
