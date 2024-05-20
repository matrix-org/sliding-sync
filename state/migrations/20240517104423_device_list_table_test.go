package migrations

import (
	"context"
	"reflect"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
)

func TestDeviceListTableMigration(t *testing.T) {
	ctx := context.Background()
	db, close := connectToDB(t)
	defer close()

	// Create the table in the old format (data = JSONB instead of BYTEA)
	// and insert some data: we'll make sure that this data is preserved
	// after migrating.
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS syncv3_device_data (
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		data BYTEA NOT NULL,
		UNIQUE(user_id, device_id)
	);`)
	if err != nil {
		t.Fatalf("failed to create table: %s", err)
	}

	// insert old data
	rowData := []OldDeviceData{
		{
			DeviceLists: OldDeviceLists{
				New:  map[string]int{"@bob:localhost": 2},
				Sent: map[string]int{},
			},
			ChangedBits:      2,
			OTKCounts:        map[string]int{"bar": 42},
			FallbackKeyTypes: []string{"narp"},
			DeviceID:         "ALICE",
			UserID:           "@alice:localhost",
		},
		{
			DeviceLists: OldDeviceLists{
				New:  map[string]int{"@💣:localhost": 1, "@bomb:localhost": 2},
				Sent: map[string]int{"@sent:localhost": 1},
			},
			OTKCounts:        map[string]int{"foo": 100},
			FallbackKeyTypes: []string{"yep"},
			DeviceID:         "BOB",
			UserID:           "@bob:localhost",
		},
	}
	for _, data := range rowData {
		blob, err := cbor.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.ExecContext(ctx, `INSERT INTO syncv3_device_data (user_id, device_id, data) VALUES ($1, $2, $3)`, data.UserID, data.DeviceID, blob)
		if err != nil {
			t.Fatal(err)
		}
	}

	// now migrate and ensure we didn't lose any data
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = upDeviceListTable(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.Commit()

	wantSents := []internal.DeviceData{
		{
			UserID:   "@alice:localhost",
			DeviceID: "ALICE",
			DeviceKeyData: internal.DeviceKeyData{
				OTKCounts: internal.MapStringInt{
					"bar": 42,
				},
				FallbackKeyTypes: []string{"narp"},
				ChangedBits:      2,
			},
		},
		{
			UserID:   "@bob:localhost",
			DeviceID: "BOB",
			DeviceListChanges: internal.DeviceListChanges{
				DeviceListChanged: []string{"@sent:localhost"},
			},
			DeviceKeyData: internal.DeviceKeyData{
				OTKCounts: internal.MapStringInt{
					"foo": 100,
				},
				FallbackKeyTypes: []string{"yep"},
			},
		},
	}

	table := state.NewDeviceDataTable(db)
	for _, wantSent := range wantSents {
		gotSent, err := table.Select(wantSent.UserID, wantSent.DeviceID, false)
		if err != nil {
			t.Fatal(err)
		}
		assertVal(t, "'sent' data was corrupted during the migration", *gotSent, wantSent)
	}

	wantNews := []internal.DeviceData{
		{
			UserID:   "@alice:localhost",
			DeviceID: "ALICE",
			DeviceListChanges: internal.DeviceListChanges{
				DeviceListLeft: []string{"@bob:localhost"},
			},
			DeviceKeyData: internal.DeviceKeyData{
				OTKCounts: internal.MapStringInt{
					"bar": 42,
				},
				FallbackKeyTypes: []string{"narp"},
				ChangedBits:      2,
			},
		},
		{
			UserID:   "@bob:localhost",
			DeviceID: "BOB",
			DeviceListChanges: internal.DeviceListChanges{
				DeviceListChanged: []string{"@💣:localhost"},
				DeviceListLeft:    []string{"@bomb:localhost"},
			},
			DeviceKeyData: internal.DeviceKeyData{
				OTKCounts: internal.MapStringInt{
					"foo": 100,
				},
				FallbackKeyTypes: []string{"yep"},
			},
		},
	}

	for _, wantNew := range wantNews {
		gotNew, err := table.Select(wantNew.UserID, wantNew.DeviceID, true)
		if err != nil {
			t.Fatal(err)
		}
		assertVal(t, "'new' data was corrupted during the migration", *gotNew, wantNew)
	}

}

func assertVal(t *testing.T, msg string, got, want interface{}) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got\n%#v\nwant\n%#v", msg, got, want)
	}
}
