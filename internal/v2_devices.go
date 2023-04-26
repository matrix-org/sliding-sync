package internal

import "fmt"

// TODO: should this live in package sync2?

func IsNewDeviceID(deviceID string) bool {
	for _, char := range deviceID {
		if char == '\x1f' {
			return true
		}
	}
	return false
}

func ProxyDeviceID(userID, hsDeviceID string) string {
	// ASCII 1F is "Unit Separator"
	return fmt.Sprintf("%s\x1f%s", userID, hsDeviceID)
}
