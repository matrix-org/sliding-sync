package state

import (
	"database/sql"
	"encoding/json"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sqlutil"
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
func (t *DeviceDataTable) Select(userID, deviceID string, swap bool) (dd *internal.DeviceData, err error) {
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		var row DeviceDataRow
		err = t.db.Get(&row, `SELECT data FROM syncv3_device_data WHERE user_id=$1 AND device_id=$2`, userID, deviceID)
		if err != nil {
			if err == sql.ErrNoRows {
				// if there is no device data for this user, it's not an error.
				return nil
			}
			return err
		}
		// unmarshal to swap
		var tempDD internal.DeviceData
		if err = json.Unmarshal(row.Data, &tempDD); err != nil {
			return err
		}
		tempDD.UserID = userID
		tempDD.DeviceID = deviceID
		if !swap {
			dd = &tempDD
			return nil // don't swap
		}
		// swap over the fields
		n := tempDD.DeviceLists.New
		tempDD.DeviceLists.Sent = n
		tempDD.DeviceLists.New = make(map[string]int)

		// re-marshal and write
		data, err := json.Marshal(tempDD)
		if err != nil {
			return err
		}
		_, err = t.db.Exec(`UPDATE syncv3_device_data SET data=$1 WHERE user_id=$2 AND device_id=$3`, data, userID, deviceID)
		dd = &tempDD
		return err
	})
	return
}

func (t *DeviceDataTable) SelectFrom(pos int64) (results []internal.DeviceData, nextPos int64, err error) {
	nextPos = pos
	var rows []DeviceDataRow
	err = t.db.Select(&rows, `SELECT id, user_id, device_id, data FROM syncv3_device_data WHERE id > $1 ORDER BY id ASC`, pos)
	if err != nil {
		return
	}
	results = make([]internal.DeviceData, len(rows))
	for i := range rows {
		var dd internal.DeviceData
		if err = json.Unmarshal(rows[i].Data, &dd); err != nil {
			return
		}
		dd.UserID = rows[i].UserID
		dd.DeviceID = rows[i].DeviceID
		results[i] = dd
		nextPos = rows[i].ID
	}
	return
}

// Upsert combines what is in the database for this user|device with the partial entry `dd`
func (t *DeviceDataTable) Upsert(dd *internal.DeviceData) (pos int64, err error) {
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		// select what already exists
		var row DeviceDataRow
		err = t.db.Get(&row, `SELECT data FROM syncv3_device_data WHERE user_id=$1 AND device_id=$2`, dd.UserID, dd.DeviceID)
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
		}
		if dd.OTKCounts != nil {
			tempDD.OTKCounts = dd.OTKCounts
		}
		tempDD.DeviceLists = tempDD.DeviceLists.Combine(dd.DeviceLists)

		data, err := json.Marshal(tempDD)
		if err != nil {
			return err
		}
		err = t.db.QueryRow(
			`INSERT INTO syncv3_device_data(user_id, device_id, data) VALUES($1,$2,$3)
			ON CONFLICT (user_id, device_id) DO UPDATE SET data=$3, id=nextval('syncv3_device_data_seq') RETURNING id`,
			dd.UserID, dd.DeviceID, data,
		).Scan(&pos)
		return err
	})
	return
}
