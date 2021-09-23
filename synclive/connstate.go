package synclive

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/matrix-org/sync-v3/state"
	"github.com/tidwall/gjson"
)

// ConnState tracks all high-level connection state for this connection, like the combined request
// and the underlying sorted room list. It doesn't track session IDs or positions of the connection.
type ConnState struct {
	muxedReq          *Request
	userID            string
	sortedJoinedRooms []*Room
	roomSubscriptions map[string]*Room
}

func NewConnState(userID string) *ConnState {
	return &ConnState{
		userID:            userID,
		roomSubscriptions: make(map[string]*Room),
	}
}

func (c *ConnState) PushNewEvent(raw json.RawMessage, roomID, evType string, stateKey *string, content gjson.Result) {

}

func (s *ConnState) OnIncomingRequest(ctx context.Context, req *Request) (*Response, error) {
	var prevRange SliceRanges
	if s.muxedReq != nil {
		prevRange = s.muxedReq.Rooms
	}
	subs := make(map[string]RoomSubscription)
	var unsubs []string
	if s.muxedReq == nil {
		s.muxedReq = req
		subs = req.RoomSubscriptions
		unsubs = req.UnsubscribeRooms
	} else {
		combinedReq, subRooms, unsubRooms := s.muxedReq.ApplyDelta(req)
		for _, roomID := range subRooms {
			subs[roomID] = s.muxedReq.RoomSubscriptions[roomID]
		}
		s.muxedReq = combinedReq
		unsubs = unsubRooms
	}
	// TODO: update room subscriptions
	// TODO: calculate the M values for N < M calcs
	fmt.Println("subs", subs, "unsubs", unsubs, "range", s.muxedReq.Rooms, "prev_range", prevRange)

	if prevRange != nil && !prevRange.Same(s.muxedReq.Rooms) {
		// range has changed
	}

	// check if the ranges have changed. If there are new ranges, track them and send them back

	return nil, nil
}

func (s *ConnState) UserID() string {
	return s.userID
}

// Load the current state of rooms from storage based on the request parameters
func LoadRooms(s *state.Storage, req *Request, roomIDs []string) (map[string]Room, error) {
	return nil, nil
}
