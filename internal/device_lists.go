package internal

const (
	DeviceListChanged = 1
	DeviceListLeft    = 2
)

type DeviceLists struct {
	// map user_id -> DeviceList enum
	New  map[string]int `json:"n"`
	Sent map[string]int `json:"s"`
}

func (dl DeviceLists) Combine(newer DeviceLists) DeviceLists {
	n := dl.New
	if n == nil {
		n = make(map[string]int)
	}
	for k, v := range newer.New {
		n[k] = v
	}
	s := dl.Sent
	if s == nil {
		s = make(map[string]int)
	}
	for k, v := range newer.Sent {
		s[k] = v
	}
	return DeviceLists{
		New:  n,
		Sent: s,
	}
}

func ToDeviceListChangesMap(changed, left []string) map[string]int {
	if len(changed) == 0 && len(left) == 0 {
		return nil
	}
	m := make(map[string]int)
	for _, userID := range changed {
		m[userID] = DeviceListChanged
	}
	for _, userID := range left {
		m[userID] = DeviceListLeft
	}
	return m
}

func DeviceListChangesArrays(m map[string]int) (changed, left []string) {
	changed = make([]string, 0)
	left = make([]string, 0)
	for userID, state := range m {
		switch state {
		case DeviceListChanged:
			changed = append(changed, userID)
		case DeviceListLeft:
			left = append(left, userID)
		}
	}
	return
}
