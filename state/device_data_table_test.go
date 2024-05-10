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
	if w.DeviceLists.Sent != nil {
		assertVal(t, "DeviceLists.Sent", g.DeviceLists.Sent, w.DeviceLists.Sent)
	}
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
			OTKCounts: map[string]int{
				"foo": 100,
				"bar": 92,
			},
		},
		{
			UserID:           userID,
			DeviceID:         deviceID,
			FallbackKeyTypes: []string{"foobar"},
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
		},
	}

	// apply them
	for _, dd := range deltas {
		err := table.Upsert(&dd)
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
			OTKCounts: map[string]int{
				"foo": 99,
			},
			FallbackKeyTypes: []string{"foobar"},
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
		OTKCounts: map[string]int{
			"foo": 99,
		},
		FallbackKeyTypes: []string{"foobar"},
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
		OTKCounts: map[string]int{
			"foo": 99,
		},
		FallbackKeyTypes: []string{"foobar"},
	}
	assertDeviceData(t, *got, want)
}

// Tests the DeviceLists field
func TestDeviceDataTableDeviceList(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	table := NewDeviceDataTable(db)
	userID := "@TestDeviceDataTableDeviceList"
	deviceID := "BOB"

	// these are individual updates from Synapse from /sync v2
	deltas := []internal.DeviceData{
		{
			UserID:   userID,
			DeviceID: deviceID,
			DeviceLists: internal.DeviceLists{
				New: internal.ToDeviceListChangesMap([]string{"alice"}, nil),
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
	// apply them
	for _, dd := range deltas {
		err := table.Upsert(&dd)
		assertNoError(t, err)
	}

	// check we can read-only select. This doesn't modify any fields.
	for i := 0; i < 3; i++ {
		got, err := table.Select(userID, deviceID, false)
		assertNoError(t, err)
		assertDeviceData(t, *got, internal.DeviceData{
			UserID:   userID,
			DeviceID: deviceID,
			DeviceLists: internal.DeviceLists{
				Sent: internal.MapStringInt{}, // until we "swap" we don't consume the New entries
			},
		})
	}
	// now swap-er-roo, which shifts everything from New into Sent.
	got, err := table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertDeviceData(t, *got, internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			Sent: internal.ToDeviceListChangesMap([]string{"alice", "💣"}, nil),
		},
	})

	// this is permanent, read-only views show this too.
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertDeviceData(t, *got, internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			Sent: internal.ToDeviceListChangesMap([]string{"alice", "💣"}, nil),
		},
	})

	// We now expect empty DeviceLists, as we swapped twice.
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertDeviceData(t, *got, internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			Sent: internal.MapStringInt{},
		},
	})

	// get back the original state
	assertNoError(t, err)
	for _, dd := range deltas {
		err = table.Upsert(&dd)
		assertNoError(t, err)
	}
	// Move original state to Sent by swapping
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertDeviceData(t, *got, internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			Sent: internal.ToDeviceListChangesMap([]string{"alice", "💣"}, nil),
		},
	})
	// Add new entries to New before acknowledging Sent
	err = table.Upsert(&internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			New: internal.ToDeviceListChangesMap([]string{"💣"}, []string{"charlie"}),
		},
	})
	assertNoError(t, err)

	// Reading without swapping does not move New->Sent, so returns the previous value
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertDeviceData(t, *got, internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			Sent: internal.ToDeviceListChangesMap([]string{"alice", "💣"}, nil),
		},
	})

	// Append even more items to New
	err = table.Upsert(&internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			New: internal.ToDeviceListChangesMap([]string{"dave"}, []string{"dave"}),
		},
	})
	assertNoError(t, err)

	// Now swap: all the combined items in New go into Sent
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertDeviceData(t, *got, internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			Sent: internal.ToDeviceListChangesMap([]string{"💣", "dave"}, []string{"charlie", "dave"}),
		},
	})

	// Swapping again clears Sent out, and since nothing is in New we get an empty list
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertDeviceData(t, *got, internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			Sent: internal.MapStringInt{},
		},
	})

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
