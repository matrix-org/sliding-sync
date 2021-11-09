package sync2

import (
	"os"
	"testing"

	"github.com/matrix-org/sync-v3/testutils"
)

var postgresConnectionString = "user=xxxxx dbname=syncv3_test sslmode=disable"

func TestMain(m *testing.M) {
	postgresConnectionString = testutils.PrepareDBConnectionString()
	exitCode := m.Run()
	os.Exit(exitCode)
}

func TestStorage(t *testing.T) {
	deviceID := "TEST_DEVICE_ID"
	store := NewStore(postgresConnectionString)
	device, err := store.InsertDevice(deviceID)
	if err != nil {
		t.Fatalf("Failed to InsertDevice: %s", err)
	}
	assertEqual(t, device.DeviceID, deviceID, "Device.DeviceID mismatch")
	if err = store.UpdateDeviceSince(deviceID, "s1"); err != nil {
		t.Fatalf("UpdateDeviceSince returned error: %s", err)
	}
	if err = store.UpdateUserIDForDevice(deviceID, "@alice:localhost"); err != nil {
		t.Fatalf("UpdateUserIDForDevice returned error: %s", err)
	}

	// now check that device retrieval has the latest values
	device, err = store.Device(deviceID)
	if err != nil {
		t.Fatalf("Device returned error: %s", err)
	}
	assertEqual(t, device.DeviceID, deviceID, "Device.DeviceID mismatch")
	assertEqual(t, device.Since, "s1", "Device.Since mismatch")
	assertEqual(t, device.UserID, "@alice:localhost", "Device.UserID mismatch")

	// now check new devices remember the v2 since value and user ID
	s2, err := store.InsertDevice(deviceID)
	if err != nil {
		t.Fatalf("InsertDevice returned error: %s", err)
	}
	assertEqual(t, s2.Since, "s1", "Device.Since mismatch")
	assertEqual(t, s2.UserID, "@alice:localhost", "Device.UserID mismatch")
	assertEqual(t, s2.DeviceID, deviceID, "Device.DeviceID mismatch")
}

func assertEqual(t *testing.T, got, want, msg string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %s want %s", msg, got, want)
	}
}
