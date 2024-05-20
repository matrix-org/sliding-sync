package internal

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

// DeviceData contains useful data for this user's device.
type DeviceData struct {
	DeviceListChanges
	DeviceKeyData
	UserID   string
	DeviceID string
}

// This is calculated from device_lists table
type DeviceListChanges struct {
	DeviceListChanged []string
	DeviceListLeft    []string
}

// This gets serialised as CBOR in device_data table
type DeviceKeyData struct {
	// Contains the latest device_one_time_keys_count values.
	// Set whenever this field arrives down the v2 poller, and it replaces what was previously there.
	OTKCounts MapStringInt `json:"otk"`
	// Contains the latest device_unused_fallback_key_types value
	// Set whenever this field arrives down the v2 poller, and it replaces what was previously there.
	// If this is a nil slice this means no change. If this is an empty slice then this means the fallback key was used up.
	FallbackKeyTypes []string `json:"fallback"`
	// bitset for which device data changes are present. They accumulate until they get swapped over
	// when they get reset
	ChangedBits int `json:"c"`
}

func (dd *DeviceKeyData) SetOTKCountChanged() {
	dd.ChangedBits = setBit(dd.ChangedBits, bitOTKCount)
}

func (dd *DeviceKeyData) SetFallbackKeysChanged() {
	dd.ChangedBits = setBit(dd.ChangedBits, bitFallbackKeyTypes)
}

func (dd *DeviceKeyData) OTKCountChanged() bool {
	return isBitSet(dd.ChangedBits, bitOTKCount)
}
func (dd *DeviceKeyData) FallbackKeysChanged() bool {
	return isBitSet(dd.ChangedBits, bitFallbackKeyTypes)
}
