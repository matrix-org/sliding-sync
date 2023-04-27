package sync2

import (
	"os"
	"sort"
	"testing"

	"github.com/matrix-org/sliding-sync/testutils"
)

var postgresConnectionString = "user=xxxxx dbname=syncv3_test sslmode=disable"

func TestMain(m *testing.M) {
	postgresConnectionString = testutils.PrepareDBConnectionString()
	exitCode := m.Run()
	os.Exit(exitCode)
}

func TestStorage(t *testing.T) {
	deviceID := "ALICE"
	accessToken := "my_access_token"
	store := NewStore(postgresConnectionString, "my_secret")
	device, err := store.InsertDevice(deviceID, accessToken)
	if err != nil {
		t.Fatalf("Failed to InsertDevice: %s", err)
	}
	assertEqual(t, device.DeviceID, deviceID, "Device.DeviceID mismatch")
	assertEqual(t, device.AccessToken, accessToken, "Device.AccessToken mismatch")
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
	assertEqual(t, device.AccessToken, accessToken, "Device.AccessToken mismatch")

	// now check new devices remember the v2 since value and user ID
	s2, err := store.InsertDevice(deviceID, accessToken)
	if err != nil {
		t.Fatalf("InsertDevice returned error: %s", err)
	}
	assertEqual(t, s2.Since, "s1", "Device.Since mismatch")
	assertEqual(t, s2.UserID, "@alice:localhost", "Device.UserID mismatch")
	assertEqual(t, s2.DeviceID, deviceID, "Device.DeviceID mismatch")
	assertEqual(t, s2.AccessToken, accessToken, "Device.AccessToken mismatch")

	// check all devices works
	deviceID2 := "BOB"
	accessToken2 := "BOB_ACCESS_TOKEN"
	bobDevice, err := store.InsertDevice(deviceID2, accessToken2)
	if err != nil {
		t.Fatalf("InsertDevice returned error: %s", err)
	}
	devices, err := store.AllDevices()
	if err != nil {
		t.Fatalf("AllDevices: %s", err)
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].DeviceID < devices[j].DeviceID
	})
	wantDevices := []*Device{
		device, bobDevice,
	}
	if len(devices) != len(wantDevices) {
		t.Fatalf("AllDevices: got %d devices, want %d", len(devices), len(wantDevices))
	}
	for i := range devices {
		assertEqual(t, devices[i].Since, wantDevices[i].Since, "Device.Since mismatch")
		assertEqual(t, devices[i].UserID, wantDevices[i].UserID, "Device.UserID mismatch")
		assertEqual(t, devices[i].DeviceID, wantDevices[i].DeviceID, "Device.DeviceID mismatch")
		assertEqual(t, devices[i].AccessToken, wantDevices[i].AccessToken, "Device.AccessToken mismatch")
	}
}

func assertEqual(t *testing.T, got, want, msg string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %s want %s", msg, got, want)
	}
}
