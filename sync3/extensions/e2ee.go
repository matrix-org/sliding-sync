package extensions

import (
	"github.com/matrix-org/sync-v3/sync2"
)

// Client created request params
type E2EERequest struct {
	Enabled bool `json:"enabled"`
}

func (r E2EERequest) ApplyDelta(next *E2EERequest) *E2EERequest {
	r.Enabled = next.Enabled
	return &r
}

// Server response
type E2EEResponse struct {
	OTKCounts   map[string]int  `json:"device_one_time_keys_count"`
	DeviceLists *E2EEDeviceList `json:"device_lists,omitempty"`
}

type E2EEDeviceList struct {
	Changed []string `json:"changed"`
	Left    []string `json:"left"`
}

func (r *E2EEResponse) HasData(isInitial bool) bool {
	if isInitial {
		return true // ensure we send OTK counts immediately
	}
	// OTK counts aren't enough to make /sync return early as we send them liberally, not just on change
	return r.DeviceLists != nil
}

func ProcessE2EE(fetcher sync2.E2EEFetcher, userID, deviceID string, req *E2EERequest) (res *E2EEResponse) {
	//  pull OTK counts and changed/left from v2 poller
	otkCounts, changed, left := fetcher.LatestE2EEData(deviceID)
	res = &E2EEResponse{
		OTKCounts: otkCounts,
	}
	if len(changed) > 0 || len(left) > 0 {
		res.DeviceLists = &E2EEDeviceList{
			Changed: changed,
			Left:    left,
		}
		logger.Info().Int("changed", len(changed)).Int("left", len(left)).Str("user", userID).Msg("E2EE extension: new data")
	}
	return
}
