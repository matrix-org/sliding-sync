package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sqlutil"
)

type DeviceDataRow struct {
	ID       int64  `db:"id"`
	UserID   string `db:"user_id"`
	DeviceID string `db:"device_id"`
	// This will contain internal.DeviceData serialised as JSON. It's stored in a single column as we don't
	// need to perform searches on this data.
	Data []byte `db:"data"`
}

type DeviceDataTable struct {
	db *sqlx.DB
}

func NewDeviceDataTable(db *sqlx.DB) *DeviceDataTable {
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_device_data (
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		data JSONB NOT NULL,
		UNIQUE(user_id, device_id)
	);
	-- Set the fillfactor to 90%, to allow for HOT updates (e.g. we only
	-- change the data, not anything indexed like the id)
	ALTER TABLE syncv3_device_data SET (fillfactor = 90);
	`)
	return &DeviceDataTable{
		db: db,
	}
}

// Atomically select the device data for this user|device and then swap DeviceLists around if set.
// This should only be called by the v3 HTTP APIs when servicing an E2EE extension request.
func (t *DeviceDataTable) Select(userID, deviceID string, swap bool) (result *internal.DeviceData, err error) {
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		var row DeviceDataRow
		err = txn.Get(&row, `SELECT data FROM syncv3_device_data WHERE user_id=$1 AND device_id=$2`, userID, deviceID)
		if err != nil {
			if err == sql.ErrNoRows {
				// if there is no device data for this user, it's not an error.
				return nil
			}
			return err
		}
		// unmarshal to swap
		if err = json.Unmarshal(row.Data, &result); err != nil {
			return err
		}
		result.UserID = userID
		result.DeviceID = deviceID
		if !swap {
			return nil // don't swap
		}
		// swap over the fields
		writeBack := *result
		writeBack.DeviceLists.Sent = result.DeviceLists.New
		writeBack.DeviceLists.New = make(map[string]int)
		writeBack.ChangedBits = 0

		// DeepEqual uses fewer allocations and is faster than
		// json.Marshal and comparing bytes
		if reflect.DeepEqual(writeBack, *result) {
			// The update to the DB would be a no-op; don't bother with it.
			// This helps reduce write usage and the contention on the unique index for
			// the device_data table.
			return nil
		}

		// Some JSON juggling in Postgres ahead. This is to avoid pushing
		// DeviceLists.Sent -> DeviceLists.New over the wire again.
		_, err = txn.Exec(`UPDATE syncv3_device_data SET data = jsonb_set(
    		jsonb_set(data, '{dl,s}', data->'dl'->'n', false), -- move 'dl.n' -> 'dl.s'
    		'{dl,n}',  '{}', false) || '{"c":0}' -- clear 'dl.n' and set the changed bits to 0
			WHERE user_id = $1 AND device_id = $2`, userID, deviceID)
		return err
	})
	return
}

func (t *DeviceDataTable) DeleteDevice(userID, deviceID string) error {
	_, err := t.db.Exec(`DELETE FROM syncv3_device_data WHERE user_id = $1 AND device_id = $2`, userID, deviceID)
	return err
}

// Upsert combines what is in the database for this user|device with the partial entry `dd`
func (t *DeviceDataTable) Upsert(dd *internal.DeviceData) (err error) {
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		// see if anything already exists
		var changedBit int
		err = txn.QueryRow(`SELECT data->>'c' FROM syncv3_device_data WHERE user_id=$1 AND device_id=$2 FOR UPDATE`, dd.UserID, dd.DeviceID).Scan(&changedBit)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("failed to check if device data exists: %v", err)
		}
		// brand new insert, do it and return
		if err == sql.ErrNoRows {
			// we need to tell postgres these fields are objects not arrays, else || won't behave as we want
			if dd.DeviceLists.New == nil {
				dd.DeviceLists.New = make(internal.MapStringInt)
			}
			if dd.DeviceLists.Sent == nil {
				dd.DeviceLists.Sent = make(internal.MapStringInt)
			}
			newInsert := internal.DeviceData{
				DeviceLists: dd.DeviceLists,
			}
			if dd.FallbackKeyTypes != nil {
				newInsert.FallbackKeyTypes = dd.FallbackKeyTypes
				newInsert.SetFallbackKeysChanged()
			}
			if dd.OTKCounts != nil {
				newInsert.OTKCounts = dd.OTKCounts
				newInsert.SetOTKCountChanged()
			}
			data, err := json.Marshal(newInsert)
			if err != nil {
				return fmt.Errorf("failed to marshal new device data: %v", err)
			}
			_, err = txn.Exec(
				`INSERT INTO syncv3_device_data(user_id, device_id, data) VALUES($1,$2,$3)
				ON CONFLICT (user_id, device_id) DO UPDATE SET data=$3`,
				dd.UserID, dd.DeviceID, data,
			)
			return err
		}
		dd.ChangedBits = changedBit

		// figure out which keys have changed and just update that section of the JSON
		if dd.FallbackKeyTypes != nil {
			_, err = txn.Exec(`UPDATE syncv3_device_data SET data = jsonb_set(data, '{fallback}', to_jsonb($3::text[]), true) WHERE user_id = $1 AND device_id = $2`, dd.UserID, dd.DeviceID, pq.StringArray(dd.FallbackKeyTypes))
			if err != nil {
				return fmt.Errorf("failed to set fallback keys: %v", err)
			}
			dd.SetFallbackKeysChanged()
		}
		if dd.OTKCounts != nil {
			_, err = txn.Exec(`UPDATE syncv3_device_data SET data = jsonb_set(data, '{otk}', $3, true) WHERE user_id = $1 AND device_id = $2`, dd.UserID, dd.DeviceID, dd.OTKCounts)
			if err != nil {
				return fmt.Errorf("failed to set otk counts: %v", err)
			}
			dd.SetOTKCountChanged()
		}
		if len(dd.DeviceLists.New) > 0 {
			_, err = txn.Exec(`UPDATE syncv3_device_data SET data = jsonb_set(data, '{dl,n}', data->'dl'->'n' || $3::jsonb, true) WHERE user_id = $1 AND device_id = $2`,
				dd.UserID, dd.DeviceID, dd.DeviceLists.New)
			if err != nil {
				return fmt.Errorf("failed to update device list changes: %v", err)
			}
		}

		if changedBit != dd.ChangedBits {
			_, err = txn.Exec(`UPDATE syncv3_device_data SET data = jsonb_set(data, '{c}', $3) WHERE user_id = $1 AND device_id = $2`, dd.UserID, dd.DeviceID, dd.ChangedBits)
			if err != nil {
				return fmt.Errorf("failed to set changed bit: %v", err)
			}
		}

		return nil
	})
	return
}
