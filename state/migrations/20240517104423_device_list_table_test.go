package migrations

import (
	"context"
	"testing"

	"github.com/fxamacker/cbor/v2"
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

}
