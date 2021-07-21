package sync3

import (
	"fmt"
	"strconv"
	"strings"
)

// V3_S1_57423_F9
// "V3_S" $SESSION "_" $NID "_F" $FILTER
type Token struct {
	SessionID int64
	NID       int64
	FilterID  int64
}

func (t Token) String() string {
	var filterID string
	if t.FilterID != 0 {
		filterID = fmt.Sprintf("%d", t.FilterID)
	}
	return fmt.Sprintf("V3_S%d_%d_F%s", t.SessionID, t.NID, filterID)
}

func NewSyncToken(since string) (*Token, error) {
	segments := strings.SplitN(since, "_", 4)
	if len(segments) != 4 {
		return nil, fmt.Errorf("not a sync v3 token")
	}
	if segments[0] != "V3" {
		return nil, fmt.Errorf("not a sync v3 token: %s", since)
	}
	filterstr := strings.TrimPrefix(segments[3], "F")
	var fid int64
	var err error
	if len(filterstr) > 0 {
		fid, err = strconv.ParseInt(filterstr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid filter id: %s", filterstr)
		}
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
		FilterID:  fid,
	}, nil
}
