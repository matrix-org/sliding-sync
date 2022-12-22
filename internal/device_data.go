package internal

import (
	"sync"
)

const (
	bitOTKCount int = iota
	bitFallbackKeyTypes
)

func setBit(n int, bit int) int {
	n |= (1 << bit)
	return n
}
func isBitSet(n int, bit int) bool {
	val := n & (1 << bit)
	return val > 0
}

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

	// bitset for which device data changes are present. They accumulate until they get swapped over
	// when they get reset
	ChangedBits int `json:"c"`

	UserID   string
	DeviceID string
}

func (dd *DeviceData) SetOTKCountChanged() {
	dd.ChangedBits = setBit(dd.ChangedBits, bitOTKCount)
}

func (dd *DeviceData) SetFallbackKeysChanged() {
	dd.ChangedBits = setBit(dd.ChangedBits, bitFallbackKeyTypes)
}

func (dd *DeviceData) OTKCountChanged() bool {
	return isBitSet(dd.ChangedBits, bitOTKCount)
}
func (dd *DeviceData) FallbackKeysChanged() bool {
	return isBitSet(dd.ChangedBits, bitFallbackKeyTypes)
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
