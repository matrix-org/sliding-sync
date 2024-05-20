package state

import (
	"reflect"
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
)

// Like assertValue, but this inserts newlines between got and want.
func assertVal(t *testing.T, msg string, got, want interface{}) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got\n%#v\nwant\n%#v", msg, got, want)
	}
}

func assertDeviceData(t *testing.T, g, w internal.DeviceData) {
	t.Helper()
	assertVal(t, "device id", g.DeviceID, w.DeviceID)
	assertVal(t, "user id", g.UserID, w.UserID)
	assertVal(t, "FallbackKeyTypes", g.FallbackKeyTypes, w.FallbackKeyTypes)
	assertVal(t, "OTKCounts", g.OTKCounts, w.OTKCounts)
	assertVal(t, "ChangedBits", g.ChangedBits, w.ChangedBits)
}

// Tests OTKCounts and FallbackKeyTypes behaviour
func TestDeviceDataTableOTKCountAndFallbackKeyTypes(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	table := NewDeviceDataTable(db)
	userID := "@TestDeviceDataTableOTKCountAndFallbackKeyTypes"
	deviceID := "BOB"

	// these are individual updates from Synapse from /sync v2
	deltas := []internal.DeviceData{
		{
			UserID:   userID,
			DeviceID: deviceID,
			DeviceKeyData: internal.DeviceKeyData{
				OTKCounts: map[string]int{
					"foo": 100,
					"bar": 92,
				},
			},
		},
		{
			UserID:   userID,
			DeviceID: deviceID,
			DeviceKeyData: internal.DeviceKeyData{
				FallbackKeyTypes: []string{"foobar"},
			},
		},
		{
			UserID:   userID,
			DeviceID: deviceID,
			DeviceKeyData: internal.DeviceKeyData{
				OTKCounts: map[string]int{
					"foo": 99,
				},
			},
		},
		{
			UserID:   userID,
			DeviceID: deviceID,
		},
	}

	// apply them
	for _, dd := range deltas {
		err := table.Upsert(dd.UserID, dd.DeviceID, dd.DeviceKeyData, nil)
		assertNoError(t, err)
	}

	// read them without swap, it should have replaced them correctly.
	// Because sync v2 sends the complete OTK count and complete fallback key types
	// every time, we always use the latest values. Because we aren't swapping, repeated
	// reads produce the same result.
	for i := 0; i < 3; i++ {
		got, err := table.Select(userID, deviceID, false)
		mustNotError(t, err)
		want := internal.DeviceData{
			UserID:   userID,
			DeviceID: deviceID,
			DeviceKeyData: internal.DeviceKeyData{
				OTKCounts: map[string]int{
					"foo": 99,
				},
				FallbackKeyTypes: []string{"foobar"},
			},
		}
		want.SetFallbackKeysChanged()
		want.SetOTKCountChanged()
		assertDeviceData(t, *got, want)
	}
	// now we swap the data. This still returns the same values, but the changed bits are no longer set
	// on subsequent reads.
	got, err := table.Select(userID, deviceID, true)
	mustNotError(t, err)
	want := internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceKeyData: internal.DeviceKeyData{
			OTKCounts: map[string]int{
				"foo": 99,
			},
			FallbackKeyTypes: []string{"foobar"},
		},
	}
	want.SetFallbackKeysChanged()
	want.SetOTKCountChanged()
	assertDeviceData(t, *got, want)

	// subsequent read
	got, err = table.Select(userID, deviceID, false)
	mustNotError(t, err)
	want = internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceKeyData: internal.DeviceKeyData{
			OTKCounts: map[string]int{
				"foo": 99,
			},
			FallbackKeyTypes: []string{"foobar"},
		},
	}
	assertDeviceData(t, *got, want)
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
		DeviceKeyData: internal.DeviceKeyData{
			OTKCounts: map[string]int{
				"foo": 100,
				"bar": 92,
			},
		},
	}
	fallbakKeyUpdate := internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceKeyData: internal.DeviceKeyData{
			FallbackKeyTypes: []string{"foo", "bar"},
		},
	}
	bothUpdate := internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceKeyData: internal.DeviceKeyData{
			FallbackKeyTypes: []string{"both"},
			OTKCounts: map[string]int{
				"both": 100,
			},
		},
	}

	err := table.Upsert(otkUpdate.UserID, otkUpdate.DeviceID, otkUpdate.DeviceKeyData, nil)
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
	err = table.Upsert(fallbakKeyUpdate.UserID, fallbakKeyUpdate.DeviceID, fallbakKeyUpdate.DeviceKeyData, nil)
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
	err = table.Upsert(bothUpdate.UserID, bothUpdate.DeviceID, bothUpdate.DeviceKeyData, nil)
	assertNoError(t, err)
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	bothUpdate.SetFallbackKeysChanged()
	bothUpdate.SetOTKCountChanged()
	assertDeviceData(t, *got, bothUpdate)
}
