package sync3

import (
	"fmt"
	"strconv"
	"strings"
)

// To add a new position, specify the const here then increment `totalStreamPositions` and implement
// getters/setters for the new stream position.

const (
	IndexEventPosition = iota
	IndexRoomMemberPosition
	IndexTypingPosition
	IndexToDevicePosition
)
const totalStreamPositions = 4

// V3_S1_F9_57423_123_5183
// "V3_S" $SESSION "_F" $FILTER "_" $A "_" $B "_" $C
type Token struct {
	// User associated values (this is different to sync v2 which doesn't have this concept)
	SessionID int64
	FilterID  int64

	// Server-side stream positions (same as sync v2)
	positions [totalStreamPositions]int64
}

func (t *Token) EventPosition() int64 {
	return t.positions[IndexEventPosition]
}
func (t *Token) TypingPosition() int64 {
	return t.positions[IndexTypingPosition]
}
func (t *Token) ToDevicePosition() int64 {
	return t.positions[IndexToDevicePosition]
}
func (t *Token) RoomMemberPosition() int64 {
	return t.positions[IndexRoomMemberPosition]
}
func (t *Token) SetEventPosition(pos int64) {
	t.positions[IndexEventPosition] = pos
}
func (t *Token) SetTypingPosition(pos int64) {
	t.positions[IndexTypingPosition] = pos
}
func (t *Token) SetToDevicePosition(pos int64) {
	t.positions[IndexToDevicePosition] = pos
}
func (t *Token) SetRoomMemberPosition(pos int64) {
	t.positions[IndexRoomMemberPosition] = pos
}

func (t *Token) IsAfter(x Token) bool {
	for i := range t.positions {
		if t.positions[i] > x.positions[i] {
			return true
		}
	}
	return false
}

// AssociateWithUser sets client-side data from `userToken` onto this token.
func (t *Token) AssociateWithUser(userToken Token) {
	t.SessionID = userToken.SessionID
	t.FilterID = userToken.FilterID
}

// ApplyUpdates increments the counters associated with server-side data from `other`, if and only
// if the counters in `other` are newer/higher.
func (t *Token) ApplyUpdates(other Token) {
	for i := range t.positions {
		if other.positions[i] > t.positions[i] {
			t.positions[i] = other.positions[i]
		}
	}
}

func (t *Token) String() string {
	posStr := make([]string, len(t.positions))
	for i := range t.positions {
		posStr[i] = strconv.FormatInt(t.positions[i], 10)
	}
	positions := strings.Join(posStr, "_")
	if t.FilterID == 0 {
		return fmt.Sprintf("V3_S%d_%s", t.SessionID, positions)
	}
	return fmt.Sprintf("V3_S%d_F%d_%s", t.SessionID, t.FilterID, positions)
}

func NewBlankSyncToken(sessionID, filterID int64) *Token {
	return &Token{
		SessionID: sessionID,
		FilterID:  filterID,
		positions: [totalStreamPositions]int64{},
	}
}

func NewSyncToken(since string) (*Token, error) {
	segments := strings.Split(since, "_")
	if segments[0] != "V3" {
		return nil, fmt.Errorf("not a sync v3 token: %s", since)
	}
	segments = segments[1:]
	var sessionID int64
	var filterID int64
	var positions []int64
	var err error
	for _, segment := range segments {
		if strings.HasPrefix(segment, "F") {
			filterStr := strings.TrimPrefix(segment, "F")
			filterID, err = strconv.ParseInt(filterStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid filter ID: %s", segment)
			}
		} else if strings.HasPrefix(segment, "S") {
			sessionStr := strings.TrimPrefix(segment, "S")
			sessionID, err = strconv.ParseInt(sessionStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid session: %s", segment)
			}
		} else {
			pos, err := strconv.ParseInt(segment, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid segment '%s': %s", segment, err)
			}
			positions = append(positions, pos)
		}
	}
	if len(positions) != totalStreamPositions {
		return nil, fmt.Errorf("expected %d stream positions, got %d", totalStreamPositions, len(positions))
	}
	token := &Token{
		SessionID: sessionID,
		FilterID:  filterID,
	}
	copy(token.positions[:], positions)
	return token, nil
}
