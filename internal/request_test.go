package internal

import (
	"net/http"
	"testing"
)

func TestDeviceIDFromRequest(t *testing.T) {
	req, _ := http.NewRequest("POST", "http://localhost:8008", nil)
	req.Header.Set("Authorization", "Bearer A")
	deviceIDA, _, err := HashedTokenFromRequest(req)
	if err != nil {
		t.Fatalf("HashedTokenFromRequest returned %s", err)
	}
	req.Header.Set("Authorization", "Bearer B")
	deviceIDB, _, err := HashedTokenFromRequest(req)
	if err != nil {
		t.Fatalf("HashedTokenFromRequest returned %s", err)
	}
	if deviceIDA == deviceIDB {
		t.Fatalf("HashedTokenFromRequest: hashed to same device ID: %s", deviceIDA)
	}

}
