package internal

import (
	"sync"
)

// DeviceData contains useful data for this user's device. This list can be expanded without prompting
// schema changes. These values are upserted into the database and persisted forever.
type DeviceData struct {
	// Contains the latest device_one_time_keys_count values.
	// Set whenever this field arrives down the v2 poller, and it replaces what was previously there.
	OTKCounts map[string]int `json:"otk"`
	// Contains the latest device_unused_fallback_key_types value
	// Set whenever this field arrives down the v2 poller, and it replaces what was previously there.
	FallbackKeyTypes []string `json:"fallback"`

	DeviceLists DeviceLists `json:"dl"`

	UserID   string
	DeviceID string
}

type UserDeviceKey struct {
	UserID   string
	DeviceID string
}

type DeviceDataMap struct {
	deviceDataMu  *sync.Mutex
	deviceDataMap map[UserDeviceKey]*DeviceData
	Pos           int64
}

func NewDeviceDataMap(startPos int64, devices []DeviceData) *DeviceDataMap {
	ddm := &DeviceDataMap{
		deviceDataMu:  &sync.Mutex{},
		deviceDataMap: make(map[UserDeviceKey]*DeviceData),
		Pos:           startPos,
	}
	for i, dd := range devices {
		ddm.deviceDataMap[UserDeviceKey{
			UserID:   dd.UserID,
			DeviceID: dd.DeviceID,
		}] = &devices[i]
	}
	return ddm
}

func (d *DeviceDataMap) Get(userID, deviceID string) *DeviceData {
	key := UserDeviceKey{
		UserID:   userID,
		DeviceID: deviceID,
	}
	d.deviceDataMu.Lock()
	defer d.deviceDataMu.Unlock()
	dd, ok := d.deviceDataMap[key]
	if !ok {
		return nil
	}
	return dd
}

func (d *DeviceDataMap) Update(dd DeviceData) DeviceData {
	key := UserDeviceKey{
		UserID:   dd.UserID,
		DeviceID: dd.DeviceID,
	}
	d.deviceDataMu.Lock()
	defer d.deviceDataMu.Unlock()
	existing, ok := d.deviceDataMap[key]
	if !ok {
		existing = &DeviceData{
			UserID:   dd.UserID,
			DeviceID: dd.DeviceID,
		}
	}
	if dd.OTKCounts != nil {
		existing.OTKCounts = dd.OTKCounts
	}
	if dd.FallbackKeyTypes != nil {
		existing.FallbackKeyTypes = dd.FallbackKeyTypes
	}
	existing.DeviceLists = existing.DeviceLists.Combine(dd.DeviceLists)

	d.deviceDataMap[key] = existing

	return *existing
}
