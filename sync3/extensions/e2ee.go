package extensions

import (
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync2"
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
	OTKCounts        map[string]int  `json:"device_one_time_keys_count,omitempty"`
	DeviceLists      *E2EEDeviceList `json:"device_lists,omitempty"`
	FallbackKeyTypes []string        `json:"device_unused_fallback_key_types,omitempty"`
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

func ProcessE2EE(fetcher sync2.E2EEFetcher, userID, deviceID string, req *E2EERequest, isInitial bool) (res *E2EEResponse) {
	//  pull OTK counts and changed/left from device data
	dd := fetcher.DeviceData(userID, deviceID, isInitial)
	res = &E2EEResponse{}
	if dd == nil {
		return res // unknown device?
	}
	if dd.FallbackKeyTypes != nil {
		res.FallbackKeyTypes = dd.FallbackKeyTypes
	}
	if dd.OTKCounts != nil {
		res.OTKCounts = dd.OTKCounts
	}
	changed, left := internal.DeviceListChangesArrays(dd.DeviceLists.Sent)
	if len(changed) > 0 || len(left) > 0 {
		res.DeviceLists = &E2EEDeviceList{
			Changed: changed,
			Left:    left,
		}
	}
	return
}
