package sync3

import (
	"fmt"
	"strconv"
	"strings"
)

// V3_S1_F2-3-4-5-6
// "V3_" $SESSION "_" $FILTERS
type Token struct {
	SessionID int64
	FilterIDs []string
}

func (t Token) String() string {
	filters := strings.Join(t.FilterIDs, "-")
	return fmt.Sprintf("V3_S%d_F%s", t.SessionID, filters)
}

func NewSyncToken(since string) (*Token, error) {
	segments := strings.SplitN(since, "_", 3)
	if len(segments) != 3 {
		return nil, fmt.Errorf("not a sync v3 token")
	}
	if segments[0] != "V3" {
		return nil, fmt.Errorf("not a sync v3 token: %s", since)
	}
	filters := strings.TrimPrefix(segments[2], "F")
	filterIDs := strings.Split(filters, "-")
	if len(filters) == 0 {
		filterIDs = nil
	}
	sidstr := strings.TrimPrefix(segments[1], "S")
	sid, err := strconv.ParseInt(sidstr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid session: %s", sidstr)
	}
	return &Token{
		SessionID: sid,
		FilterIDs: filterIDs,
	}, nil
}
