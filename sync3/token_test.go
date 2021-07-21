package sync3

import (
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
			// with filter
			in: "V3_S1_12_F6",
			outToken: &Token{
				SessionID: 1,
				NID:       12,
				FilterID:  6,
			},
		},
		{
			// without filter
			in: "V3_S1_33_F",
			outToken: &Token{
				SessionID: 1,
				NID:       33,
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
		if tc.outToken.FilterID != gotTok.FilterID {
			t.Errorf("test case %+v wrong filter ID: got %+v want %+v", tc, gotTok.FilterID, tc.outToken.FilterID)
		}
	}
}
