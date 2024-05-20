package sync2

import (
	"net/url"
	"testing"
)

func TestSyncURL(t *testing.T) {
	baseURL := "https://atreus.gow"
	wantBaseURL := baseURL + "/_matrix/client/r0/sync"
	client := HTTPClient{
		DestinationServer: baseURL,
	}
	testCases := []struct {
		since        string
		isFirst      bool
		toDeviceOnly bool
		wantURL      string
	}{
		{
			since:        "",
			isFirst:      false,
			toDeviceOnly: false,
			wantURL:      wantBaseURL + `?timeout=30000&set_presence=offline&filter=` + url.QueryEscape(`{"presence":{"not_types":["*"]},"room":{"timeline":{"limit":1}}}`),
		},
		{
			since:        "",
			isFirst:      true,
			toDeviceOnly: false,
			wantURL:      wantBaseURL + `?timeout=0&set_presence=offline&filter=` + url.QueryEscape(`{"presence":{"not_types":["*"]},"room":{"timeline":{"limit":1}}}`),
		},
		{
			since:        "",
			isFirst:      false,
			toDeviceOnly: true,
			wantURL:      wantBaseURL + `?timeout=30000&set_presence=offline&filter=` + url.QueryEscape(`{"presence":{"not_types":["*"]},"room":{"rooms":[],"timeline":{"limit":1}}}`),
		},
		{
			since:        "",
			isFirst:      true,
			toDeviceOnly: true,
			wantURL:      wantBaseURL + `?timeout=0&set_presence=offline&filter=` + url.QueryEscape(`{"presence":{"not_types":["*"]},"room":{"rooms":[],"timeline":{"limit":1}}}`),
		},
		{
			since:        "112233",
			isFirst:      false,
			toDeviceOnly: false,
			wantURL:      wantBaseURL + `?timeout=30000&since=112233&set_presence=offline&filter=` + url.QueryEscape(`{"presence":{"not_types":["*"]},"room":{"timeline":{"limit":50}}}`),
		},
		{
			since:        "112233",
			isFirst:      true,
			toDeviceOnly: false,
			wantURL:      wantBaseURL + `?timeout=0&since=112233&set_presence=offline&filter=` + url.QueryEscape(`{"presence":{"not_types":["*"]},"room":{"timeline":{"limit":50}}}`),
		},
		{
			since:        "112233",
			isFirst:      false,
			toDeviceOnly: true,
			wantURL:      wantBaseURL + `?timeout=30000&since=112233&set_presence=offline&filter=` + url.QueryEscape(`{"presence":{"not_types":["*"]},"room":{"rooms":[],"timeline":{"limit":50}}}`),
		},
		{
			since:        "112233",
			isFirst:      true,
			toDeviceOnly: true,
			wantURL:      wantBaseURL + `?timeout=0&since=112233&set_presence=offline&filter=` + url.QueryEscape(`{"presence":{"not_types":["*"]},"room":{"rooms":[],"timeline":{"limit":50}}}`),
		},
		{
			since:        "112233#145",
			isFirst:      true,
			toDeviceOnly: true,
			wantURL:      wantBaseURL + `?timeout=0&since=112233%23145&set_presence=offline&filter=` + url.QueryEscape(`{"presence":{"not_types":["*"]},"room":{"rooms":[],"timeline":{"limit":50}}}`),
		},
	}
	for i, tc := range testCases {
		gotURL := client.createSyncURL(tc.since, tc.isFirst, tc.toDeviceOnly)
		if gotURL != tc.wantURL {
			t.Errorf("Case %d/%d: got %v want %v", i+1, len(testCases), gotURL, tc.wantURL)
		}
	}
}
