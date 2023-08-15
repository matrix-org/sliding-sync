package migrations

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	_ "github.com/lib/pq"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
)

func TestCBORBMigration(t *testing.T) {
	ctx := context.Background()
	db, close := connectToDB(t)
	defer close()

	// Create the table in the old format (data = JSONB instead of BYTEA)
	// and insert some data: we'll make sure that this data is preserved
	// after migrating.
	_, err := db.Exec(`CREATE TABLE syncv3_device_data (
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		data JSONB NOT NULL,
		UNIQUE(user_id, device_id)
	);`)

	if err != nil {
		t.Fatal(err)
	}

	rowData := []internal.DeviceData{
		{
			DeviceLists: internal.DeviceLists{
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
			DeviceLists: internal.DeviceLists{
				New:  map[string]int{"@💣:localhost": 1, "@bomb:localhost": 2},
				Sent: map[string]int{"@sent:localhost": 1},
			},
			OTKCounts:        map[string]int{"foo": 100},
			FallbackKeyTypes: []string{"yep"},
			DeviceID:         "BOB",
			UserID:           "@bob:localhost",
		},
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}

	for _, dd := range rowData {
		data, err := json.Marshal(dd)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO syncv3_device_data (user_id, device_id, data) VALUES ($1, $2, $3)`, dd.UserID, dd.DeviceID, data)
		if err != nil {
			t.Fatal(err)
		}
	}

	// validate that invalid data can be migrated upwards
	err = upCborDeviceData(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.Commit()

	// ensure we can now select it
	table := state.NewDeviceDataTable(db)
	for _, want := range rowData {
		got, err := table.Select(want.UserID, want.DeviceID, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(*got, want) {
			t.Fatalf("got  %+v\nwant %+v", *got, want)
		}
	}

	// and downgrade again
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = downCborDeviceData(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}

	// ensure it is what we originally inserted
	for _, want := range rowData {
		var got internal.DeviceData
		var gotBytes []byte
		err = tx.QueryRow(`SELECT data FROM syncv3_device_data WHERE user_id=$1 AND device_id=$2`, want.UserID, want.DeviceID).Scan(&gotBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(gotBytes, &got); err != nil {
			t.Fatal(err)
		}
		got.DeviceID = want.DeviceID
		got.UserID = want.UserID
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got  %+v\nwant %+v", got, want)
		}
	}

	tx.Commit()
}
