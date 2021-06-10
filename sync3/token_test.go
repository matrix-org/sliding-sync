package sync3

import (
	"reflect"
	"testing"
)

func TestNewSyncToken(t *testing.T) {
	testCases := []struct {
		in       string
		outToken *Token
		outErr   bool
	}{
		{
			// bogus data
			in:     "invalid",
			outErr: true,
		},
		{
			// v2 token
			in:     "s2082982339_757284961_6072131_904809235_806324855_2395127_276932481_1135951226_200599",
			outErr: true,
		},
		{
			// with filters
			in: "V3_S1_F2-3-4-5-6",
			outToken: &Token{
				SessionID: "1",
				FilterIDs: []string{"2", "3", "4", "5", "6"},
			},
		},
		{
			// without filters
			in: "V3_S1_F",
			outToken: &Token{
				SessionID: "1",
			},
		},
	}
	for _, tc := range testCases {
		gotTok, gotErr := NewSyncToken(tc.in)
		if (gotErr != nil && !tc.outErr) || (gotErr == nil && tc.outErr) {
			t.Errorf("test case %+v unexpected error value: %v want error=%v", tc, gotErr, tc.outErr)
			continue
		}
		if gotTok == nil {
			continue
		}
		if tc.in != gotTok.String() {
			t.Errorf("test case %+v token did not pass through parsing, got %v want %v", tc, gotTok.String(), tc.in)
		}
		if tc.outToken.SessionID != gotTok.SessionID {
			t.Errorf("test case %+v wrong session ID: got %v want %v", tc, gotTok.SessionID, tc.outToken.SessionID)
		}
		if len(tc.outToken.FilterIDs) != len(gotTok.FilterIDs) {
			t.Errorf("test case %+v wrong number of filters: got %d want %d", tc, len(gotTok.FilterIDs), len(tc.outToken.FilterIDs))
		}
		if !reflect.DeepEqual(tc.outToken.FilterIDs, gotTok.FilterIDs) {
			t.Errorf("test case %+v wrong filter IDs: got %+v want %+v", tc, gotTok.FilterIDs, tc.outToken.FilterIDs)
		}
	}
}
