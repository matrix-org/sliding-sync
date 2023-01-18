package extensions

import (
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

// Fetcher used by the E2EE extension
type E2EEFetcher interface {
	DeviceData(userID, deviceID string, isInitial bool) *internal.DeviceData
}

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
	return r.DeviceLists != nil || len(r.FallbackKeyTypes) > 0 || len(r.OTKCounts) > 0
}

func ProcessLiveE2EE(up caches.Update, fetcher E2EEFetcher, userID, deviceID string, req *E2EERequest) (res *E2EEResponse) {
	_, ok := up.(caches.DeviceDataUpdate)
	if !ok {
		return nil
	}
	return ProcessE2EE(fetcher, userID, deviceID, req, false)
}

func ProcessE2EE(fetcher E2EEFetcher, userID, deviceID string, req *E2EERequest, isInitial bool) (res *E2EEResponse) {
	//  pull OTK counts and changed/left from device data
	dd := fetcher.DeviceData(userID, deviceID, isInitial)
	res = &E2EEResponse{}
	if dd == nil {
		return res // unknown device?
	}
	if dd.FallbackKeyTypes != nil && (dd.FallbackKeysChanged() || isInitial) {
		res.FallbackKeyTypes = dd.FallbackKeyTypes
	}
	if dd.OTKCounts != nil && (dd.OTKCountChanged() || isInitial) {
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
