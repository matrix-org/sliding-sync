package internal

import (
	"net/http"
	"testing"
)

func TestDeviceIDFromRequest(t *testing.T) {
	req, _ := http.NewRequest("POST", "http://localhost:8008", nil)
	req.Header.Set("Authorization", "Bearer A")
	deviceIDA, err := DeviceIDFromRequest(req)
	if err != nil {
		t.Fatalf("DeviceIDFromRequest returned %s", err)
	}
	req.Header.Set("Authorization", "Bearer B")
	deviceIDB, err := DeviceIDFromRequest(req)
	if err != nil {
		t.Fatalf("DeviceIDFromRequest returned %s", err)
	}
	if deviceIDA == deviceIDB {
		t.Fatalf("DeviceIDFromRequest: hashed to same device ID: %s", deviceIDA)
	}

}
