package state

import (
	"database/sql"
	"encoding/json"
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
		// select what already exists
		var row DeviceDataRow
		err = txn.Get(&row, `SELECT data FROM syncv3_device_data WHERE user_id=$1 AND device_id=$2`, dd.UserID, dd.DeviceID)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		// unmarshal and combine
		var tempDD internal.DeviceData
		if len(row.Data) > 0 {
			if err = json.Unmarshal(row.Data, &tempDD); err != nil {
				return err
			}
		}
		if dd.FallbackKeyTypes != nil {
			tempDD.FallbackKeyTypes = dd.FallbackKeyTypes
			tempDD.SetFallbackKeysChanged()
		}
		if dd.OTKCounts != nil {
			tempDD.OTKCounts = dd.OTKCounts
			tempDD.SetOTKCountChanged()
		}
		tempDD.DeviceLists = tempDD.DeviceLists.Combine(dd.DeviceLists)

		// we already got something in the database - update by just sending the new data
		if len(row.Data) > 0 {
			if tempDD.FallbackKeyTypes == nil {
				// If fallback is null, it would, for some reason, nuke the whole
				// column, so make sure we have something set.
				tempDD.FallbackKeyTypes = []string{}
			}
			_, err = txn.Exec(`UPDATE syncv3_device_data SET data = 
				jsonb_set(jsonb_set(jsonb_set(jsonb_set(jsonb_set(data, '{otk}', $3, false), '{fallback}', to_jsonb($4::text[]), false), '{c}', $5, false), '{dl,s}', $6, false),'{dl,n}', $7, false)
				WHERE user_id = $1 AND device_id = $2
				`,
				dd.UserID, dd.DeviceID, tempDD.OTKCounts, pq.StringArray(tempDD.FallbackKeyTypes), tempDD.ChangedBits, tempDD.DeviceLists.Sent, tempDD.DeviceLists.New,
			)
			return err
		}

		data, err := json.Marshal(tempDD)
		if err != nil {
			return err
		}
		_, err = txn.Exec(
			`INSERT INTO syncv3_device_data(user_id, device_id, data) VALUES($1,$2,$3)
			ON CONFLICT (user_id, device_id) DO UPDATE SET data=$3`,
			dd.UserID, dd.DeviceID, data,
		)
		return err
	})
	return
}
