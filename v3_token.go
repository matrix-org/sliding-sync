package syncv3

import (
	"fmt"
	"strings"
)

// V3_S1_F2-3-4-5-6_V2_s111_222_333_444
// "V3_" $SESSION "_" $FILTERS "_V2_" $V2TOKEN
type v3token struct {
	sessionID string
	filterIDs []string
	v2token   string
}

func (t v3token) String() string {
	filters := strings.Join(t.filterIDs, "-")
	return fmt.Sprintf("V3_S%s_F%s_V2_%s", t.sessionID, filters, t.v2token)
}

func newSyncV3Token(since string) (*v3token, error) {
	segments := strings.SplitN(since, "_", 5)
	if len(segments) != 5 {
		return nil, fmt.Errorf("not a sync v3 token")
	}
	if segments[0] != "V3" || segments[3] != "V2" {
		return nil, fmt.Errorf("not a sync v3 token: %s", since)
	}
	filters := strings.TrimPrefix(segments[2], "F")
	return &v3token{
		sessionID: strings.TrimPrefix(segments[1], "S"),
		filterIDs: strings.Split(filters, "-"),
		v2token:   segments[4],
	}, nil
}
