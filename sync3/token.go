package sync3

import (
	"fmt"
	"strconv"
	"strings"
)

// V3_S1_57423_F2-3-4-5-6
// "V3_" $SESSION "_" $NID "_" $FILTERS
type Token struct {
	SessionID int64
	NID       int64
	FilterIDs []string
}

func (t Token) String() string {
	filters := strings.Join(t.FilterIDs, "-")
	return fmt.Sprintf("V3_S%d_%d_F%s", t.SessionID, t.NID, filters)
}

func NewSyncToken(since string) (*Token, error) {
	segments := strings.SplitN(since, "_", 4)
	if len(segments) != 4 {
		return nil, fmt.Errorf("not a sync v3 token")
	}
	if segments[0] != "V3" {
		return nil, fmt.Errorf("not a sync v3 token: %s", since)
	}
	filters := strings.TrimPrefix(segments[3], "F")
	filterIDs := strings.Split(filters, "-")
	if len(filters) == 0 {
		filterIDs = nil
	}
	sidstr := strings.TrimPrefix(segments[1], "S")
	sid, err := strconv.ParseInt(sidstr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid session: %s", sidstr)
	}
	nid, err := strconv.ParseInt(segments[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid nid: %s", sidstr)
	}
	return &Token{
		SessionID: sid,
		NID:       nid,
		FilterIDs: filterIDs,
	}, nil
}
