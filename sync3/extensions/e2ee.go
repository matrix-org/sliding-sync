package extensions

import (
	"context"

	"github.com/matrix-org/sliding-sync/internal"
)

// Fetcher used by the E2EE extension
type E2EEFetcher interface {
	DeviceData(userID, deviceID string, isInitial bool) *internal.DeviceData
}

// Client created request params
type E2EERequest struct {
	Enableable
}

func (r *E2EERequest) Name() string {
	return "E2EERequest"
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

func (r *E2EERequest) Process(ctx context.Context, res *Response, extCtx Context) {
	//  pull OTK counts and changed/left from device data
	dd := extCtx.E2EEFetcher.DeviceData(extCtx.UserID, extCtx.DeviceID, extCtx.IsInitial)
	if dd == nil {
		return // unknown device?
	}
	extRes := &E2EEResponse{}
	hasUpdates := false
	if dd.FallbackKeyTypes != nil && (dd.FallbackKeysChanged() || extCtx.IsInitial) {
		extRes.FallbackKeyTypes = dd.FallbackKeyTypes
		hasUpdates = true
	}
	if dd.OTKCounts != nil && (dd.OTKCountChanged() || extCtx.IsInitial) {
		extRes.OTKCounts = dd.OTKCounts
		hasUpdates = true
	}
	changed, left := internal.DeviceListChangesArrays(dd.DeviceLists.Sent)
	if len(changed) > 0 || len(left) > 0 {
		extRes.DeviceLists = &E2EEDeviceList{
			Changed: changed,
			Left:    left,
		}
		hasUpdates = true
	}
	if !hasUpdates {
		return
	}
	res.E2EE = extRes // TODO: aggregate
}
