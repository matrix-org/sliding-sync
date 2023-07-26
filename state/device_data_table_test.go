package state

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
)

func assertVal(t *testing.T, msg string, got, want interface{}) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %v want %v", msg, got, want)
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

func BenchmarkReflectDeepEqual(b *testing.B) {
	newList := map[string]int{"abc": 1, "cde": 1, "fgh": 1, "ijk": 1, "lmn": 1}
	d1 := internal.DeviceData{}
	d2 := internal.DeviceData{
		DeviceLists: internal.DeviceLists{New: newList},
		DeviceID:    "abc",
		UserID:      "abc",
		ChangedBits: 0,
		OTKCounts:   map[string]int{"ed25519": 50},
	}

	for i := 0; i < b.N; i++ {
		if reflect.DeepEqual(d1, d2) {
			b.Fatal("structs are equal")
		}
	}
}

func BenchmarkJSONMarhsalBytesEqual(b *testing.B) {
	newList := map[string]int{"abc": 1, "cde": 1, "fgh": 1, "ijk": 1, "lmn": 1}
	d1 := internal.DeviceData{}
	d2 := internal.DeviceData{
		DeviceLists: internal.DeviceLists{New: newList},
		DeviceID:    "abc",
		UserID:      "abc",
		ChangedBits: 0,
		OTKCounts:   map[string]int{"ed25519": 50},
	}

	data1, err := json.Marshal(d1)
	if err != nil {
		b.Fatal(err)
	}

	// Reset the timer, so we actually just test what is
	// executed after we fetched the data from the database.
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data2, err := json.Marshal(d2)
		if err != nil {
			b.Fatal(err)
		}
		if bytes.Equal(data1, data2) {
			b.Fatal("bytes are equal")
		}
	}
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
				New: internal.ToDeviceListChangesMap([]string{"bob"}, nil),
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
			New: internal.ToDeviceListChangesMap([]string{"alice", "bob"}, nil),
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
	// now swap-er-roo
	got, err := table.Select(userID, deviceID, true)
	assertNoError(t, err)
	want2 := want
	want2.DeviceLists = internal.DeviceLists{
		Sent: internal.ToDeviceListChangesMap([]string{"alice"}, nil),
		New:  nil,
	}
	assertDeviceData(t, *got, want2)

	// changed bits were reset when we swapped
	want2.ChangedBits = 0
	want.ChangedBits = 0

	// this is permanent, read-only views show this too
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertDeviceData(t, *got, want2)

	// another swap causes sent to be cleared out
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	want3 := want2
	want3.DeviceLists = internal.DeviceLists{
		Sent: nil,
		New:  nil,
	}
	assertDeviceData(t, *got, want3)

	// get back the original state
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
	_, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	err = table.Upsert(&internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			New: internal.ToDeviceListChangesMap([]string{"bob"}, []string{"charlie"}),
		},
	})
	assertNoError(t, err)

	want.ChangedBits = 0

	want4 := want
	want4.DeviceLists = internal.DeviceLists{
		Sent: internal.ToDeviceListChangesMap([]string{"alice"}, nil),
		New:  internal.ToDeviceListChangesMap([]string{"bob"}, []string{"charlie"}),
	}
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertDeviceData(t, *got, want4)

	// another append then consume
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
		Sent: internal.ToDeviceListChangesMap([]string{"bob", "dave"}, []string{"charlie", "dave"}),
		New:  nil,
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
	}
	fallbakKeyUpdate := internal.DeviceData{
		UserID:           userID,
		DeviceID:         deviceID,
		FallbackKeyTypes: []string{"foo", "bar"},
	}
	bothUpdate := internal.DeviceData{
		UserID:           userID,
		DeviceID:         deviceID,
		FallbackKeyTypes: []string{"both"},
		OTKCounts: map[string]int{
			"both": 100,
		},
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
