package streams

import (
	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
)

const (
	MaxRoomList     = 1000
	DefaultRoomList = 100
)

var (
	DefaultRoomListSorts = []RoomListSortOrder{SortRoomListByRecency}
	KnownRoomListSorts   = map[RoomListSortOrder]bool{
		SortRoomListBySpace:   true,
		SortRoomListByRecency: true,
		SortRoomListByName:    true,
	}
)

type RoomListSortOrder string

const (
	// Sort rooms based on the most recent event in the room. Newer first.
	SortRoomListByRecency RoomListSortOrder = "by_recency"
	// Sort rooms by the space ordering
	// https://github.com/matrix-org/matrix-doc/blob/old_master/proposals/1772-groups-as-rooms.md#relationship-between-rooms-and-spaces
	SortRoomListBySpace RoomListSortOrder = "by_space_order"
	// Sort rooms by the calculated room name server-side
	// https://spec.matrix.org/unstable/client-server-api/#calculating-the-display-name-for-a-room
	SortRoomListByName RoomListSortOrder = "by_name"
)

// FilterRoomList represents a filter on the RoomList stream
type FilterRoomList struct {
	Sort   []RoomListSortOrder `json:"sort"`
	Limit  int                 `json:"limit"`
	Fields []string            `json:"fields"`
	// tracking vars
	AddPage      bool     `json:"add_page"`
	StreamingAdd bool     `json:"streaming_add"`
	AddRooms     []string `json:"add_rooms"`
	DelRooms     []string `json:"del_rooms"`

	NextPage string `json:"next_page,omitempty"`
}

func (r *FilterRoomList) Validate() {
	if r.Limit > MaxRoomList {
		r.Limit = MaxRoomList
	}
	if r.Limit <= 0 {
		r.Limit = DefaultRoomList
	}
	if r.Sort == nil {
		r.Sort = DefaultRoomListSorts
	}
	// validate the sorts
	for i := range r.Sort {
		if !KnownRoomListSorts[r.Sort[i]] {
			// remove it
			r.Sort = append(r.Sort[:i], r.Sort[i+1:]...)
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
	NextPage string `json:"next_page,omitempty"`
}

type RoomListEntry struct {
	RoomID    string      `json:"room_id"`
	Name      string      `json:"name"`
	Timestamp int64       `json:"timestamp"`
	Tag       interface{} `json:"tag"`
	// MemberCount TODO
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
	return req.RoomList != nil && req.RoomList.NextPage != ""
}

func (s *RoomList) SessionConfirmed(session *sync3.Session, confirmedPos int64, allSessions bool) {
}

func (s *RoomList) DataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) (int64, error) {
	if request.RoomList == nil {
		return 0, ErrNotRequested
	}
	request.RoomList.Validate()
	if request.RoomList.NextPage == "" && fromExcl != 0 {
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
	_, _ = s.storage.JoinedRoomsAfterPosition(session.UserID, pos)
	// find all invited / joined rooms for this user
	// populate summaries
	// offset based on P
	return nil
}

func (s *RoomList) streamingDataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) (int64, error) {
	// TODO
	return toIncl, nil
}
