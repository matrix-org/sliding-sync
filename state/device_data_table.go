package state

import (
	"bytes"
	"database/sql"
	"encoding/json"

	"github.com/jmoiron/sqlx"
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
	CREATE SEQUENCE IF NOT EXISTS syncv3_device_data_seq;
	CREATE TABLE IF NOT EXISTS syncv3_device_data (
		id BIGINT PRIMARY KEY NOT NULL DEFAULT nextval('syncv3_device_data_seq'),
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		data BYTEA NOT NULL,
		UNIQUE(user_id, device_id)
	);
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

		// re-marshal and write
		data, err := json.Marshal(writeBack)
		if err != nil {
			return err
		}
		if bytes.Equal(data, row.Data) {
			// The update to the DB would be a no-op; don't bother with it.
			// This helps reduce write usage and the contention on the unique index for
			// the device_data table.
			return nil
		}
		_, err = txn.Exec(`UPDATE syncv3_device_data SET data=$1 WHERE user_id=$2 AND device_id=$3`, data, userID, deviceID)
		return err
	})
	return
}

func (t *DeviceDataTable) DeleteDevice(userID, deviceID string) error {
	_, err := t.db.Exec(`DELETE FROM syncv3_device_data WHERE user_id = $1 AND device_id = $2`, userID, deviceID)
	return err
}

// Upsert combines what is in the database for this user|device with the partial entry `dd`
func (t *DeviceDataTable) Upsert(dd *internal.DeviceData) (pos int64, err error) {
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

		data, err := json.Marshal(tempDD)
		if err != nil {
			return err
		}
		err = txn.QueryRow(
			`INSERT INTO syncv3_device_data(user_id, device_id, data) VALUES($1,$2,$3)
			ON CONFLICT (user_id, device_id) DO UPDATE SET data=$3, id=nextval('syncv3_device_data_seq') RETURNING id`,
			dd.UserID, dd.DeviceID, data,
		).Scan(&pos)
		return err
	})
	return
}
