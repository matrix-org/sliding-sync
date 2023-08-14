package migrations

import (
	"context"
	"encoding/json"
	"testing"

	_ "github.com/lib/pq"
	"github.com/matrix-org/sliding-sync/internal"
)

func TestCBORBMigration(t *testing.T) {
	ctx := context.Background()
	db, close := connectToDB(t)
	defer close()

	// Create the table in the old format (data = JSONB instead of BYTEA)
	_, err := db.Exec(`CREATE TABLE syncv3_device_data (
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		data JSONB NOT NULL,
		UNIQUE(user_id, device_id)
	);`)

	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Commit()

	// insert some "invalid" data
	dd := internal.DeviceData{
		DeviceLists: internal.DeviceLists{
			New:  map[string]int{"@💣:localhost": 1},
			Sent: map[string]int{},
		},
		OTKCounts:        map[string]int{},
		FallbackKeyTypes: []string{},
	}
	data, err := json.Marshal(dd)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO syncv3_device_data (user_id, device_id, data) VALUES ($1, $2, $3)`, "bob", "bobDev", data)
	if err != nil {
		t.Fatal(err)
	}

	// validate that invalid data can be migrated upwards
	err = upCborDeviceData(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}

	// and downgrade again
	err = downCborDeviceData(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
}
