package sync3

import (
	"fmt"
	"strings"
)

// V3_S1_F2-3-4-5-6_V2_s111_222_333_444
// "V3_" $SESSION "_" $FILTERS "_V2_" $V2TOKEN
type Token struct {
	SessionID string
	FilterIDs []string
	V2token   string
}

func (t Token) String() string {
	filters := strings.Join(t.FilterIDs, "-")
	return fmt.Sprintf("V3_S%s_F%s_V2_%s", t.SessionID, filters, t.V2token)
}

func NewSyncToken(since string) (*Token, error) {
	segments := strings.SplitN(since, "_", 5)
	if len(segments) != 5 {
		return nil, fmt.Errorf("not a sync v3 token")
	}
	if segments[0] != "V3" || segments[3] != "V2" {
		return nil, fmt.Errorf("not a sync v3 token: %s", since)
	}
	filters := strings.TrimPrefix(segments[2], "F")
	return &Token{
		SessionID: strings.TrimPrefix(segments[1], "S"),
		FilterIDs: strings.Split(filters, "-"),
		V2token:   segments[4],
	}, nil
}
