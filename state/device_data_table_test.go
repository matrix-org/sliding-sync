package state

import (
	"reflect"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sync-v3/internal"
)

func assertVal(t *testing.T, msg string, got, want interface{}) {
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %v want %v", msg, got, want)
	}
}

func assertDeviceDatas(t *testing.T, got, want []internal.DeviceData) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d devices, want %d : %+v", len(got), len(want), got)
	}
	for i := range want {
		g := got[i]
		w := want[i]
		assertVal(t, "device id", g.DeviceID, w.DeviceID)
		assertVal(t, "user id", g.UserID, w.UserID)
		assertVal(t, "FallbackKeyTypes", g.FallbackKeyTypes, w.FallbackKeyTypes)
		assertVal(t, "OTKCounts", g.OTKCounts, w.OTKCounts)
	}
}

func TestDeviceDataTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	table := NewDeviceDataTable(db)
	userID := "@alice"
	deviceID := "ALICE"
	dd := &internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		OTKCounts: map[string]int{
			"foo": 100,
		},
		FallbackKeyTypes: []string{"foo", "bar"},
	}

	// test basic insert -> select
	pos, err := table.Upsert(dd)
	assertNoError(t, err)
	results, nextPos, err := table.SelectFrom(-1)
	assertNoError(t, err)
	if pos != nextPos {
		t.Fatalf("Upsert returned pos %v but SelectFrom returned pos %v", pos, nextPos)
	}
	assertDeviceDatas(t, results, []internal.DeviceData{*dd})

	// at latest -> no results
	results, nextPos, err = table.SelectFrom(nextPos)
	assertNoError(t, err)
	if pos != nextPos {
		t.Fatalf("Upsert returned pos %v but SelectFrom returned pos %v", pos, nextPos)
	}
	assertDeviceDatas(t, results, nil)

	// multiple insert -> replace on user|device
	dd2 := *dd
	dd2.OTKCounts = map[string]int{"foo": 99}
	_, err = table.Upsert(&dd2)
	assertNoError(t, err)
	dd3 := *dd
	dd3.OTKCounts = map[string]int{"foo": 98}
	pos, err = table.Upsert(&dd3)
	assertNoError(t, err)
	results, nextPos, err = table.SelectFrom(nextPos)
	assertNoError(t, err)
	if pos != nextPos {
		t.Fatalf("Upsert returned pos %v but SelectFrom returned pos %v", pos, nextPos)
	}
	assertDeviceDatas(t, results, []internal.DeviceData{dd3})

	// multiple insert -> different user, same device + same user, different device
	dd4 := *dd
	dd4.UserID = "@bob"
	_, err = table.Upsert(&dd4)
	assertNoError(t, err)
	dd5 := *dd
	dd5.DeviceID = "ANOTHER"
	pos, err = table.Upsert(&dd5)
	assertNoError(t, err)
	results, nextPos, err = table.SelectFrom(nextPos)
	assertNoError(t, err)
	if pos != nextPos {
		t.Fatalf("Upsert returned pos %v but SelectFrom returned pos %v", pos, nextPos)
	}
	assertDeviceDatas(t, results, []internal.DeviceData{dd4, dd5})
}

func TestDeviceDataTableSwaps(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
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
		_, err = table.Upsert(&dd)
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
	// check we can read-only select
	for i := 0; i < 3; i++ {
		got, err := table.Select(userID, deviceID, false)
		assertNoError(t, err)
		assertDeviceDatas(t, []internal.DeviceData{*got}, []internal.DeviceData{want})
	}
	// now swap-er-roo
	got, err := table.Select(userID, deviceID, true)
	assertNoError(t, err)
	want2 := want
	want2.DeviceLists = internal.DeviceLists{
		Sent: internal.ToDeviceListChangesMap([]string{"alice"}, nil),
		New:  nil,
	}
	assertDeviceDatas(t, []internal.DeviceData{*got}, []internal.DeviceData{want2})

	// this is permanent, read-only views show this too
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertDeviceDatas(t, []internal.DeviceData{*got}, []internal.DeviceData{want2})

	// another swap causes sent to be cleared out
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	want3 := want2
	want3.DeviceLists = internal.DeviceLists{
		Sent: nil,
		New:  nil,
	}
	assertDeviceDatas(t, []internal.DeviceData{*got}, []internal.DeviceData{want3})

	// get back the original state
	for _, dd := range deltas {
		_, err = table.Upsert(&dd)
		assertNoError(t, err)
	}
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertDeviceDatas(t, []internal.DeviceData{*got}, []internal.DeviceData{want})

	// swap once then add once so both sent and new are populated
	_, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	_, err = table.Upsert(&internal.DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		DeviceLists: internal.DeviceLists{
			New: internal.ToDeviceListChangesMap([]string{"bob"}, []string{"charlie"}),
		},
	})
	assertNoError(t, err)

	want4 := want
	want4.DeviceLists = internal.DeviceLists{
		Sent: internal.ToDeviceListChangesMap([]string{"alice"}, nil),
		New:  internal.ToDeviceListChangesMap([]string{"bob"}, []string{"charlie"}),
	}
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertDeviceDatas(t, []internal.DeviceData{*got}, []internal.DeviceData{want4})

	// another append then consume
	_, err = table.Upsert(&internal.DeviceData{
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
	assertDeviceDatas(t, []internal.DeviceData{*got}, []internal.DeviceData{want5})

}
