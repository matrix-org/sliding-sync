package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/matrix-org/sliding-sync/sqlutil"
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
	for _, want := range rowData {
		got, err := OldDeviceDataTableSelect(db, want.UserID, want.DeviceID, false)
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
		var got OldDeviceData
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

type OldDeviceDataRow struct {
	ID       int64  `db:"id"`
	UserID   string `db:"user_id"`
	DeviceID string `db:"device_id"`
	// This will contain internal.DeviceData serialised as JSON. It's stored in a single column as we don't
	// need to perform searches on this data.
	Data []byte `db:"data"`
}

func OldDeviceDataTableSelect(db *sqlx.DB, userID, deviceID string, swap bool) (result *OldDeviceData, err error) {
	err = sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		var row OldDeviceDataRow
		err = txn.Get(&row, `SELECT data FROM syncv3_device_data WHERE user_id=$1 AND device_id=$2 FOR UPDATE`, userID, deviceID)
		if err != nil {
			if err == sql.ErrNoRows {
				// if there is no device data for this user, it's not an error.
				return nil
			}
			return err
		}
		// unmarshal to swap
		opts := cbor.DecOptions{
			MaxMapPairs: 1000000000, // 1 billion :(
		}
		decMode, err := opts.DecMode()
		if err != nil {
			return err
		}
		if err = decMode.Unmarshal(row.Data, &result); err != nil {
			return err
		}
		result.UserID = userID
		result.DeviceID = deviceID
		if !swap {
			return nil // don't swap
		}
		// the caller will only look at sent, so make sure what is new is now in sent
		result.DeviceLists.Sent = result.DeviceLists.New

		// swap over the fields
		writeBack := *result
		writeBack.DeviceLists.Sent = result.DeviceLists.New
		writeBack.DeviceLists.New = make(map[string]int)
		writeBack.ChangedBits = 0

		if reflect.DeepEqual(result, &writeBack) {
			// The update to the DB would be a no-op; don't bother with it.
			// This helps reduce write usage and the contention on the unique index for
			// the device_data table.
			return nil
		}
		// re-marshal and write
		data, err := cbor.Marshal(writeBack)
		if err != nil {
			return err
		}

		_, err = txn.Exec(`UPDATE syncv3_device_data SET data=$1 WHERE user_id=$2 AND device_id=$3`, data, userID, deviceID)
		return err
	})
	return
}
