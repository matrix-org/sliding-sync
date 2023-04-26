package sync2

import (
	"github.com/matrix-org/sliding-sync/internal"
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
	alice := "@alice:localhost"
	aliceHSDeviceID := "ALICE"
	aliceDeviceID := internal.ProxyDeviceID(alice, aliceHSDeviceID)
	aliceAccessToken := "my_access_token"
	store := NewStore(postgresConnectionString, "my_secret")
	device, err := store.InsertDevice(alice, aliceDeviceID, aliceAccessToken)
	if err != nil {
		t.Fatalf("Failed to InsertDevice: %s", err)
	}
	assertEqual(t, device.DeviceID, aliceDeviceID, "Device.DeviceID mismatch")
	assertEqual(t, device.AccessToken, aliceAccessToken, "Device.AccessToken mismatch")
	if err = store.UpdateDeviceSince(aliceDeviceID, "s1"); err != nil {
		t.Fatalf("UpdateDeviceSince returned error: %s", err)
	}

	// now check that device retrieval has the latest values
	device, err = store.Device(aliceDeviceID)
	if err != nil {
		t.Fatalf("Device returned error: %s", err)
	}
	assertEqual(t, device.DeviceID, aliceDeviceID, "Device.DeviceID mismatch")
	assertEqual(t, device.Since, "s1", "Device.Since mismatch")
	assertEqual(t, device.UserID, alice, "Device.UserID mismatch")
	assertEqual(t, device.AccessToken, aliceAccessToken, "Device.AccessToken mismatch")

	// now check that we correctly upsert: we should remember the v2 since value and user ID
	s2, err := store.InsertDevice(alice, aliceDeviceID, aliceAccessToken)
	if err != nil {
		t.Fatalf("InsertDevice returned error: %s", err)
	}
	assertEqual(t, s2.DeviceID, aliceDeviceID, "Device.DeviceID mismatch")
	assertEqual(t, s2.Since, "s1", "Device.Since mismatch")
	assertEqual(t, s2.UserID, alice, "Device.UserID mismatch")
	assertEqual(t, s2.AccessToken, aliceAccessToken, "Device.AccessToken mismatch")

	// check all devices works
	bob := "@bob:localhost"
	bobDeviceID := "BOB"
	bobAccessToken := "BOB_ACCESS_TOKEN"
	bobDevice, err := store.InsertDevice(bob, bobDeviceID, bobAccessToken)
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

	// check that alice can update her device with a new access token
	aliceAccessToken2 := "let me in please"
	s3, err := store.InsertDevice(alice, aliceDeviceID, aliceAccessToken2)
	if err != nil {
		t.Fatalf("InsertDevice returned error: %s", err)
	}
	assertEqual(t, s3.DeviceID, aliceDeviceID, "Device.DeviceID mismatch")
	assertEqual(t, s3.Since, "s1", "Device.Since mismatch")
	assertEqual(t, s3.UserID, alice, "Device.UserID mismatch")
	assertEqual(t, s3.AccessToken, aliceAccessToken2, "Device.AccessToken mismatch")

	// check that fetching the device by ID afterwards returns the new access token
	s4, err := store.Device(aliceDeviceID)
	if err != nil {
		t.Fatalf("Device returned error: %s", err)
	}
	assertEqual(t, s4.DeviceID, aliceDeviceID, "Device.DeviceID mismatch")
	assertEqual(t, s4.Since, "s1", "Device.Since mismatch")
	assertEqual(t, s4.UserID, alice, "Device.UserID mismatch")
	assertEqual(t, s4.AccessToken, aliceAccessToken2, "Device.AccessToken mismatch")

}

func assertEqual(t *testing.T, got, want, msg string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %s want %s", msg, got, want)
	}
}
