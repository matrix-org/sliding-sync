package internal

import "testing"

func TestIsNewDeviceID(t *testing.T) {
	proxyID := ProxyDeviceID("@alice:test", "example")
	if !IsNewDeviceID(proxyID) {
		t.Fatalf("%s was not considered a new-style device ID", proxyID)
	}
}
