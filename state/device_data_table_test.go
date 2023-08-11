package state

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
)

func assertVal(t *testing.T, msg string, got, want interface{}) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got\n%#v want\n%#v", msg, got, want)
	}
}

func assertDeviceData(t *testing.T, g, w internal.DeviceData) {
	t.Helper()
	assertVal(t, "device id", g.DeviceID, w.DeviceID)
	assertVal(t, "user id", g.UserID, w.UserID)
	assertVal(t, "FallbackKeyTypes", g.FallbackKeyTypes, w.FallbackKeyTypes)
	assertVal(t, "OTKCounts", g.OTKCounts, w.OTKCounts)
	assertVal(t, "ChangedBits", g.ChangedBits, w.ChangedBits)
	assertVal(t, "DeviceLists", g.DeviceLists, w.DeviceLists)
}

func TestDeviceDataTableSwaps(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	table := NewDeviceDataTable(db)
	userID := "@bob"
	deviceID := "BOB"

	// test accumulating deltas
	deltas := []internal.DeviceData{
		{
			UserID:   userID,
			DeviceID: deviceID,
			OTKCounts: map[string]int{
				"foo": 100,
				"bar": 92,
			},
		},
		{
			UserID:           userID,
			DeviceID:         deviceID,
			FallbackKeyTypes: []string{"foobar"},
			DeviceLists: internal.DeviceLists{
				New: internal.ToDeviceListChangesMap([]string{"alice"}, nil),
			},
		},
		{
			UserID:   userID,
			DeviceID: deviceID,
			OTKCounts: map[string]int{
				"foo": 99,
			},
		},
		{
			UserID:   userID,
			DeviceID: deviceID,
			DeviceLists: internal.DeviceLists{
				New: internal.ToDeviceListChangesMap([]string{"💣"}, nil),
			},
		},
	}
	for _, dd := range deltas {
		err := table.Upsert(&dd)
		assertNoError(t, err)
	}

	want := internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		OTKCounts: map[string]int{
			"foo": 99,
		},
		FallbackKeyTypes: []string{"foobar"},
		DeviceLists: internal.DeviceLists{
			New:  internal.ToDeviceListChangesMap([]string{"alice", "💣"}, nil),
			Sent: map[string]int{},
		},
	}
	want.SetFallbackKeysChanged()
	want.SetOTKCountChanged()
	// check we can read-only select
	for i := 0; i < 3; i++ {
		got, err := table.Select(userID, deviceID, false)
		assertNoError(t, err)
		assertDeviceData(t, *got, want)
	}
	// now swap-er-roo, at this point we still expect the "old" data,
	// as it is the first time we swap
	got, err := table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertDeviceData(t, *got, want)

	// changed bits were reset when we swapped
	want2 := want
	want2.DeviceLists = internal.DeviceLists{
		Sent: internal.ToDeviceListChangesMap([]string{"alice", "💣"}, nil),
		New:  map[string]int{},
	}
	want2.ChangedBits = 0
	want.ChangedBits = 0

	// this is permanent, read-only views show this too.
	// Since we have swapped previously, we now expect New to be empty
	// and Sent to be set. Swap again to clear Sent.
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertDeviceData(t, *got, want2)

	// We now expect empty DeviceLists, as we swapped twice.
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	want3 := want2
	want3.DeviceLists = internal.DeviceLists{
		Sent: map[string]int{},
		New:  map[string]int{},
	}
	assertDeviceData(t, *got, want3)

	// get back the original state
	//err = table.DeleteDevice(userID, deviceID)
	assertNoError(t, err)
	for _, dd := range deltas {
		err = table.Upsert(&dd)
		assertNoError(t, err)
	}
	want.SetFallbackKeysChanged()
	want.SetOTKCountChanged()
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertDeviceData(t, *got, want)

	// swap once then add once so both sent and new are populated
	// Moves Alice and Bob to Sent
	_, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	err = table.Upsert(&internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			New: internal.ToDeviceListChangesMap([]string{"💣"}, []string{"charlie"}),
		},
	})
	assertNoError(t, err)

	want.ChangedBits = 0

	want4 := want
	want4.DeviceLists = internal.DeviceLists{
		Sent: internal.ToDeviceListChangesMap([]string{"alice", "💣"}, nil),
		New:  internal.ToDeviceListChangesMap([]string{"💣"}, []string{"charlie"}),
	}
	// Without swapping, we expect Alice and Bob in Sent, and Bob and Charlie in New
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertDeviceData(t, *got, want4)

	// another append then consume
	// This results in dave to be added to New
	err = table.Upsert(&internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			New: internal.ToDeviceListChangesMap([]string{"dave"}, []string{"dave"}),
		},
	})
	assertNoError(t, err)
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	want5 := want4
	want5.DeviceLists = internal.DeviceLists{
		Sent: internal.ToDeviceListChangesMap([]string{"alice", "💣"}, nil),
		New:  internal.ToDeviceListChangesMap([]string{"💣"}, []string{"charlie", "dave"}),
	}
	assertDeviceData(t, *got, want5)

	// Swapping again clears New
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	want5 = want4
	want5.DeviceLists = internal.DeviceLists{
		Sent: internal.ToDeviceListChangesMap([]string{"💣"}, []string{"charlie", "dave"}),
		New:  map[string]int{},
	}
	assertDeviceData(t, *got, want5)

	// delete everything, no data returned
	assertNoError(t, table.DeleteDevice(userID, deviceID))
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	if got != nil {
		t.Errorf("wanted no data, got %v", got)
	}
}

func TestDeviceDataTableBitset(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	table := NewDeviceDataTable(db)
	userID := "@bobTestDeviceDataTableBitset"
	deviceID := "BOBTestDeviceDataTableBitset"
	otkUpdate := internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		OTKCounts: map[string]int{
			"foo": 100,
			"bar": 92,
		},
		DeviceLists: internal.DeviceLists{New: map[string]int{}, Sent: map[string]int{}},
	}
	fallbakKeyUpdate := internal.DeviceData{
		UserID:           userID,
		DeviceID:         deviceID,
		FallbackKeyTypes: []string{"foo", "bar"},
		DeviceLists:      internal.DeviceLists{New: map[string]int{}, Sent: map[string]int{}},
	}
	bothUpdate := internal.DeviceData{
		UserID:           userID,
		DeviceID:         deviceID,
		FallbackKeyTypes: []string{"both"},
		OTKCounts: map[string]int{
			"both": 100,
		},
		DeviceLists: internal.DeviceLists{New: map[string]int{}, Sent: map[string]int{}},
	}

	err := table.Upsert(&otkUpdate)
	assertNoError(t, err)
	got, err := table.Select(userID, deviceID, true)
	assertNoError(t, err)
	otkUpdate.SetOTKCountChanged()
	assertDeviceData(t, *got, otkUpdate)
	// second time swapping causes no OTKs as there have been no changes
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	otkUpdate.ChangedBits = 0
	assertDeviceData(t, *got, otkUpdate)
	// now same for fallback keys, but we won't swap them so it should return those diffs
	err = table.Upsert(&fallbakKeyUpdate)
	assertNoError(t, err)
	fallbakKeyUpdate.OTKCounts = otkUpdate.OTKCounts
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	fallbakKeyUpdate.SetFallbackKeysChanged()
	assertDeviceData(t, *got, fallbakKeyUpdate)
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	fallbakKeyUpdate.SetFallbackKeysChanged()
	assertDeviceData(t, *got, fallbakKeyUpdate)
	// updating both works
	err = table.Upsert(&bothUpdate)
	assertNoError(t, err)
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	bothUpdate.SetFallbackKeysChanged()
	bothUpdate.SetOTKCountChanged()
	assertDeviceData(t, *got, bothUpdate)
}

func TestDeviceDataTableBadStoredData(t *testing.T) {
	// we know that the data column is a JSON object but we don't know if any of the keys are sensible values.
	// This test ensures that Select work with missing/null/explicit empty values in the DB.
	// We know for sure that:
	// - the 'c' changed bit is always present and is an integer.
	// - the 'dl' key is always present and is an object.
	db, close := connectToDB(t)
	defer close()
	table := NewDeviceDataTable(db)

	partialUpdateFallbackKeys := &internal.DeviceData{FallbackKeyTypes: []string{"foo"}}
	checkFallbackKeys := func(d *internal.DeviceData) error {
		if !reflect.DeepEqual(d.FallbackKeyTypes, partialUpdateFallbackKeys.FallbackKeyTypes) {
			return fmt.Errorf("got %v want %v", d.FallbackKeyTypes, partialUpdateFallbackKeys.FallbackKeyTypes)
		}
		return nil
	}

	partialUpdateOTKCounts := &internal.DeviceData{OTKCounts: map[string]int{"foo": 100}}
	checkOTKCounts := func(d *internal.DeviceData) error {
		if !reflect.DeepEqual(d.OTKCounts, partialUpdateOTKCounts.OTKCounts) {
			return fmt.Errorf("got %v want %v", d.OTKCounts, partialUpdateOTKCounts.OTKCounts)
		}
		return nil
	}

	// we don't test Sent lists as we never Upsert sent values: they only get set when swapping from New
	partialUpdateNewDeviceList := &internal.DeviceData{
		DeviceLists: internal.DeviceLists{
			New: map[string]int{"foo": internal.DeviceListChanged},
		},
	}
	checkNewDeviceList := func(d *internal.DeviceData) error {
		if !reflect.DeepEqual(d.DeviceLists.New, partialUpdateNewDeviceList.DeviceLists.New) {
			return fmt.Errorf("got %v want %v", d.DeviceLists.New, partialUpdateNewDeviceList.DeviceLists.New)
		}
		return nil
	}

	testCases := []struct {
		name          string
		jsonBlob      string
		partialUpsert *internal.DeviceData
		check         func(d *internal.DeviceData) error
	}{
		{
			name:          "existing fallback keys",
			jsonBlob:      `{"c":0,"dl":{},"fallback":["hello there"]}`,
			partialUpsert: partialUpdateFallbackKeys,
			check:         checkFallbackKeys,
		},
		{
			name:          "missing fallback keys",
			jsonBlob:      `{"c":0,"dl":{}}`,
			partialUpsert: partialUpdateFallbackKeys,
			check:         checkFallbackKeys,
		},
		{
			name:          "null fallback keys",
			jsonBlob:      `{"c":0,"dl":{},"fallback":null}`,
			partialUpsert: partialUpdateFallbackKeys,
			check:         checkFallbackKeys,
		},
		{
			name:          "explicit empty fallback keys",
			jsonBlob:      `{"c":0,"dl":{},"fallback":[]}`,
			partialUpsert: partialUpdateFallbackKeys,
			check:         checkFallbackKeys,
		},
		{
			name:          "existing otk key",
			jsonBlob:      `{"c":0,"dl":{},"otk":{"a":55}}`,
			partialUpsert: partialUpdateOTKCounts,
			check:         checkOTKCounts,
		},
		{
			name:          "missing otk key",
			jsonBlob:      `{"c":0,"dl":{}}`,
			partialUpsert: partialUpdateOTKCounts,
			check:         checkOTKCounts,
		},
		{
			name:          "null otk key",
			jsonBlob:      `{"c":0,"dl":{},"otk":null}`,
			partialUpsert: partialUpdateOTKCounts,
			check:         checkOTKCounts,
		},
		{
			name:          "explicit empty otk key",
			jsonBlob:      `{"c":0,"dl":{},"otk":{}}`,
			partialUpsert: partialUpdateOTKCounts,
			check:         checkOTKCounts,
		},
		// missing / null device list key is not possible
		// missing / null "n" and "s" keys are not possible.
		{
			name:          "explicit empty new device list key",
			jsonBlob:      `{"c":0,"dl":{"n":{}}}`,
			partialUpsert: partialUpdateNewDeviceList,
			check:         checkNewDeviceList,
		},
	}
	for i, tc := range testCases {
		userID := fmt.Sprintf("@TestDeviceDataTableInsertWithBadData_user%d:localhost", i)
		deviceID := fmt.Sprintf("TestDeviceDataTableInsertWithBadData_DEVICE_%d", i)
		_, err := db.Exec(
			`INSERT INTO syncv3_device_data(user_id, device_id, data) VALUES($1,$2,$3)
			ON CONFLICT (user_id, device_id) DO UPDATE SET data=$3`,
			userID, deviceID, tc.jsonBlob,
		)
		if err != nil {
			t.Fatalf("%s: failed to insert: %s", tc.name, err)
		}
		tc.partialUpsert.DeviceID = deviceID
		tc.partialUpsert.UserID = userID
		err = table.Upsert(tc.partialUpsert)
		if err != nil {
			t.Fatalf("%s: failed to Upsert: %s", tc.name, err)
		}
		got, err := table.Select(userID, deviceID, false)
		if err != nil {
			t.Fatalf("%s: failed to Select: %s", tc.name, err)
		}
		if err = tc.check(got); err != nil {
			t.Errorf("%s: check failed: %s", tc.name, err)
		}
	}
}
