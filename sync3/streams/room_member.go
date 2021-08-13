package streams

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/tidwall/gjson"
)

const (
	DefaultRoomMemberLimit = 50
	MaxRoomMemberLimit     = 1000
)

type FilterRoomMember struct {
	// Negotiated: the max number of member events to return.
	Limit int `json:"limit"`
	// The room to return the member list for.
	RoomID string `json:"room_id"`
	// The sort order to use when returning results.
	SortBy RoomMemberSortOrder `json:"sort"`
	// The pagination parameters to request the next page of results.
	P *P `json:"p,omitempty"`
}

type RoomMemberResponse struct {
	// Negotiated: The actual limit the server used.
	Limit int `json:"limit"`
	// The m.room.member events
	Events []json.RawMessage `json:"events"`
	// The pagination parameters to request the next page, can be empty if all members fit on one page.
	P *P `json:"p,omitempty"`
}

type RoomMemberSortOrder string
type membershipEnum int

var (
	sortRoomMemberByPL   RoomMemberSortOrder   = "by_pl"
	sortRoomMemberByName RoomMemberSortOrder   = "by_name"
	roomMemberSortOrders []RoomMemberSortOrder = []RoomMemberSortOrder{
		sortRoomMemberByPL,
		sortRoomMemberByName,
	}
	defaultRoomMemberSortOrder = sortRoomMemberByPL
)

const (
	invite membershipEnum = iota + 1
	join
	leave
	ban
	knock
)

func membershipEnumForString(s string) membershipEnum {
	switch s {
	case "invite":
		return invite
	case "join":
		return join
	case "ban":
		return ban
	case "knock":
		return knock
	}
	return 0
}

type memberEvent struct {
	PL         int64           // for sorting by PL
	Name       string          // for sorting by Name
	Membership membershipEnum  // for sorting by Membership
	JSON       json.RawMessage // The data
}

// RoomMember represents a stream of room members.
type RoomMember struct {
	storage *state.Storage
}

func NewRoomMember(s *state.Storage) *RoomMember {
	return &RoomMember{s}
}

func (s *RoomMember) Position(tok *sync3.Token) int64 {
	return tok.EventPosition()
}

func (s *RoomMember) SetPosition(tok *sync3.Token, pos int64) {
	tok.SetEventPosition(pos)
}

func (s *RoomMember) IsPaginationRequest(req *Request) bool {
	return req.RoomMember != nil && req.RoomMember.P != nil && req.RoomMember.P.Next != ""
}

func (s *RoomMember) SessionConfirmed(session *sync3.Session, confirmedPos int64, allSessions bool) {
}

// Extract a chunk of room members from this stream. This stream can operate in 2 modes: paginated and streaming.
//  * If `Request.RoomMember.P` is non-empty, operate in pagination mode and see what page of results to return for `fromExcl`.
//  * If `Request.RoomMember.P` is empty, operate in streaming mode and return the delta between `fromExcl` and `toIncl` (as-is normal)
//
// More specifically, streaming mode is active if and only if `fromExcl` is non-zero (not first sync) and `p` is empty. This will
// then return a delta between `fromExcl` and `toIncl`. Otherwise, it operates in paginated mode. This means the first request from a
// new client is always a paginated request, leaving it up to the client to either pull all members then stream or keep tracking the first
// page of result via the use of FirstPage sentinel value.
func (s *RoomMember) DataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) (int64, error) {
	if request.RoomMember == nil {
		return 0, ErrNotRequested
	}
	// ensure limit is always set
	if request.RoomMember.Limit > MaxRoomMemberLimit {
		request.RoomMember.Limit = MaxRoomMemberLimit
	}
	if request.RoomMember.Limit <= 0 {
		request.RoomMember.Limit = DefaultRoomMemberLimit
	}
	if request.RoomMember.P == nil && fromExcl != 0 {
		return s.streamingDataInRange(session, fromExcl, toIncl, request, resp)
	}

	// make sure we have a sort ordr
	if request.RoomMember.SortBy == "" {
		request.RoomMember.SortBy = defaultRoomMemberSortOrder
	}

	// validate P
	var sortOrder RoomMemberSortOrder
	for _, knownSortOrder := range roomMemberSortOrders {
		if string(request.RoomMember.SortBy) == string(knownSortOrder) {
			sortOrder = RoomMemberSortOrder(request.RoomMember.SortBy)
		}
	}

	// flesh out the response - if we have been given a position then use it, else default to the latest position (for first syncs)
	paginationPos := fromExcl
	if paginationPos == 0 {
		paginationPos = toIncl
	}
	err := s.paginatedDataAtPoint(session, paginationPos, sortOrder, request, resp)
	if err != nil {
		return 0, err
	}

	// pagination never advances the token
	return fromExcl, nil
}

func (s *RoomMember) paginatedDataAtPoint(session *sync3.Session, pos int64, sortOrder RoomMemberSortOrder, request *Request, resp *Response) error {
	// TODO: check that the user is in the room at `pos`
	// Load room state at pos
	events, err := s.storage.RoomStateAfterEventPosition(request.RoomMember.RoomID, pos)
	if err != nil {
		if err == sql.ErrNoRows {
			// this room ID doesn't exist at this position
			return nil
		}
		return fmt.Errorf("RoomStateAfterEventPosition %d - %s", pos, err)
	}
	// find the PL event
	var plContent gomatrixserverlib.PowerLevelContent
	plContent.Defaults()
	for _, ev := range events {
		evJSON := gjson.ParseBytes(ev.JSON)
		if evJSON.Get("type").Str == gomatrixserverlib.MRoomPowerLevels && evJSON.Get("state_key").Str == "" {
			if err = json.Unmarshal([]byte(evJSON.Get("content").Raw), &plContent); err != nil {
				return fmt.Errorf("RoomStateAfterEventPosition %d - failed to extract PL content: %s", pos, err)
			}
			break
		}
	}
	// collect room members and assign power levels to them
	members := make([]memberEvent, 0, len(events)) // we won't ever have more than `events` members, so it's a useful capacity to set
	for _, ev := range events {
		evJSON := gjson.ParseBytes(ev.JSON)
		evType := evJSON.Get("type").Str
		stateKey := evJSON.Get("state_key").Str
		if evType == gomatrixserverlib.MRoomMember {
			name := evJSON.Get("content.displayname").Str
			if name == "" {
				name = strings.TrimPrefix(stateKey, "@") // ensure we always have a name to sort on, but strip the '@' to sort Alice with @alice:localhost
			}
			mem := memberEvent{
				Name:       strings.ToLower(name),
				Membership: membershipEnumForString(evJSON.Get("content.membership").Str),
				PL:         plContent.UserLevel(stateKey),
				JSON:       ev.JSON,
			}
			members = append(members, mem)
		}
	}
	// now sort them based on the sort order in the request - we must sort stabley to ensure we sort the
	// same way each time we're called.
	sortByName := func(i, j int) bool {
		return members[i].Name < members[j].Name
	}
	sortByPLName := func(i, j int) bool {
		if members[i].PL > members[j].PL {
			return true // higher PLs sort earlier
		} else if members[i].PL < members[j].PL {
			return false // lower PLs sort later
		}
		// matching PLs tiebreak on the name
		return members[i].Name < members[j].Name
	}
	sortFunc := sortByName
	if sortOrder == sortRoomMemberByPL {
		sortFunc = sortByPLName
	}
	sort.SliceStable(members, sortFunc)

	// return the right subslice based on P, honouring the limit
	var page int
	if request.RoomMember.P != nil && request.RoomMember.P.Next != "" {
		page, err = strconv.Atoi(request.RoomMember.P.Next)
		if err != nil {
			return fmt.Errorf("invalid P.next: %s", err)
		}
		if page < 0 {
			return fmt.Errorf("invalid P.next: -ve number")
		}
	}

	// for a slice of 100, limit of 10:
	//   page 0 => 0-9 inclusive
	//   page 1 => 10-19 inclusive
	//   page 2 => 20-29 inclusive
	//   etc
	// in other words, return slice[$limit*page : $limit*page+$limit] with appropriate bounds checking
	// as [:] notation is inclusive:exclusive
	limit := request.RoomMember.Limit
	startIndex := limit * page
	endIndex := startIndex + limit
	if endIndex > len(members) {
		endIndex = len(members)
	}
	if startIndex > len(members) {
		return fmt.Errorf("out of bounds page %d", page)
	}
	result := members[startIndex:endIndex]
	resp.RoomMember = &RoomMemberResponse{}
	resp.RoomMember.Events = make([]json.RawMessage, len(result))
	for i := range result {
		resp.RoomMember.Events[i] = result[i].JSON
	}
	if endIndex != len(members) {
		// we aren't at the end
		resp.RoomMember.P = &P{
			Next: fmt.Sprintf("%d", page+1),
		}
	}
	return nil
}

func (s *RoomMember) streamingDataInRange(session *sync3.Session, fromExcl, toIncl int64, request *Request, resp *Response) (int64, error) {
	// TODO: check that the user is in the room at fromExcl and toIncl, else stop at the last state
	// Load the room member delta (honouring the limit) for the room
	events, upTo, err := s.storage.RoomMembershipDelta(request.RoomMember.RoomID, fromExcl, toIncl, request.RoomMember.Limit)
	if err != nil {
		return 0, err
	}
	resp.RoomMember = &RoomMemberResponse{}
	resp.RoomMember.Events = events
	return upTo, nil
}
