package state

import (
	"database/sql"
	"reflect"

	"github.com/fxamacker/cbor/v2"
	"github.com/getsentry/sentry-go"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sqlutil"
)

type DeviceDataRow struct {
	ID       int64  `db:"id"`
	UserID   string `db:"user_id"`
	DeviceID string `db:"device_id"`
	// This will contain internal.DeviceKeyData serialised as JSON. It's stored in a single column as we don't
	// need to perform searches on this data.
	KeyData []byte `db:"data"`
}

type DeviceDataTable struct {
	db              *sqlx.DB
	deviceListTable *DeviceListTable
}

func NewDeviceDataTable(db *sqlx.DB) *DeviceDataTable {
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_device_data (
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		data BYTEA NOT NULL,
		UNIQUE(user_id, device_id)
	);
	-- Set the fillfactor to 90%, to allow for HOT updates (e.g. we only
	-- change the data, not anything indexed like the id)
	ALTER TABLE syncv3_device_data SET (fillfactor = 90);
	`)
	return &DeviceDataTable{
		db:              db,
		deviceListTable: NewDeviceListTable(db),
	}
}

// Atomically select the device data for this user|device and then swap DeviceLists around if set.
// This should only be called by the v3 HTTP APIs when servicing an E2EE extension request.
func (t *DeviceDataTable) Select(userID, deviceID string, swap bool) (result *internal.DeviceData, err error) {
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		// grab otk counts and fallback key types
		var row DeviceDataRow
		err = txn.Get(&row, `SELECT data FROM syncv3_device_data WHERE user_id=$1 AND device_id=$2 FOR UPDATE`, userID, deviceID)
		if err != nil {
			if err == sql.ErrNoRows {
				// if there is no device data for this user, it's not an error.
				return nil
			}
			return err
		}
		result = &internal.DeviceData{}
		var keyData *internal.DeviceKeyData
		// unmarshal to swap
		if err = cbor.Unmarshal(row.KeyData, &keyData); err != nil {
			return err
		}
		result.UserID = userID
		result.DeviceID = deviceID
		if keyData != nil {
			result.DeviceKeyData = *keyData
		}

		deviceListChanges, err := t.deviceListTable.SelectTx(txn, userID, deviceID, swap)
		if err != nil {
			return err
		}
		for targetUserID, targetState := range deviceListChanges {
			switch targetState {
			case internal.DeviceListChanged:
				result.DeviceListChanged = append(result.DeviceListChanged, targetUserID)
			case internal.DeviceListLeft:
				result.DeviceListLeft = append(result.DeviceListLeft, targetUserID)
			}
		}
		if !swap {
			return nil // don't swap
		}
		// swap over the fields
		writeBack := *keyData
		writeBack.ChangedBits = 0

		if reflect.DeepEqual(keyData, &writeBack) {
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

// Upsert combines what is in the database for this user|device with the partial entry `dd`
func (t *DeviceDataTable) Upsert(userID, deviceID string, keys internal.DeviceKeyData, deviceListChanges map[string]int) (err error) {
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		// Update device lists
		if err = t.deviceListTable.UpsertTx(txn, userID, deviceID, deviceListChanges); err != nil {
			return err
		}
		// select what already exists
		var row DeviceDataRow
		err = txn.Get(&row, `SELECT data FROM syncv3_device_data WHERE user_id=$1 AND device_id=$2 FOR UPDATE`, userID, deviceID)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		// unmarshal and combine
		var keyData internal.DeviceKeyData
		if len(row.KeyData) > 0 {
			if err = cbor.Unmarshal(row.KeyData, &keyData); err != nil {
				return err
			}
		}
		if keys.FallbackKeyTypes != nil {
			keyData.FallbackKeyTypes = keys.FallbackKeyTypes
			keyData.SetFallbackKeysChanged()
		}
		if keys.OTKCounts != nil {
			keyData.OTKCounts = keys.OTKCounts
			keyData.SetOTKCountChanged()
		}

		data, err := cbor.Marshal(keyData)
		if err != nil {
			return err
		}
		_, err = txn.Exec(
			`INSERT INTO syncv3_device_data(user_id, device_id, data) VALUES($1,$2,$3)
			ON CONFLICT (user_id, device_id) DO UPDATE SET data=$3`,
			userID, deviceID, data,
		)
		return err
	})
	if err != nil && err != sql.ErrNoRows {
		sentry.CaptureException(err)
	}
	return
}
