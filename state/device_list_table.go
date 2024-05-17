package state

import (
	"fmt"

	"github.com/getsentry/sentry-go"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sqlutil"
)

const (
	BucketNew  = 1
	BucketSent = 2
)

type DeviceListTable struct {
	db *sqlx.DB
}

func NewDeviceListTable(db *sqlx.DB) *DeviceListTable {
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_device_list_updates (
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		target_user_id TEXT NOT NULL,
		target_state SMALLINT NOT NULL,
		bucket SMALLINT NOT NULL,
		UNIQUE(user_id, device_id, target_user_id, bucket)
	);
	-- make an index so selecting all the rows is faster
	CREATE INDEX IF NOT EXISTS syncv3_device_list_updates_bucket_idx ON syncv3_device_list_updates(user_id, device_id, bucket);
	-- Set the fillfactor to 90%, to allow for HOT updates (e.g. we only
	-- change the data, not anything indexed like the id)
	ALTER TABLE syncv3_device_list_updates SET (fillfactor = 90);
	`)
	return &DeviceListTable{
		db: db,
	}
}

// Upsert new device list changes.
func (t *DeviceListTable) Upsert(userID, deviceID string, deviceListChanges map[string]int) (err error) {
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		for targetUserID, targetState := range deviceListChanges {
			if targetState != internal.DeviceListChanged && targetState != internal.DeviceListLeft {
				sentry.CaptureException(fmt.Errorf("DeviceListTable.Upsert invalid target_state: %d this is a programming error", targetState))
				continue
			}
			_, err = txn.Exec(
				`INSERT INTO syncv3_device_list_updates(user_id, device_id, target_user_id, target_state, bucket) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (user_id, device_id, target_user_id, bucket) DO UPDATE SET target_state=$4`,
				userID, deviceID, targetUserID, targetState, BucketNew,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		sentry.CaptureException(err)
	}
	return
}

// Select device list changes for this client. Returns a map of user_id => change enum.
func (t *DeviceListTable) Select(userID, deviceID string, swap bool) (result internal.MapStringInt, err error) {
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		if !swap {
			// read only view, just return what we previously sent and don't do anything else.
			result, err = t.selectDeviceListChangesInBucket(txn, userID, deviceID, BucketSent)
			return err
		}

		// delete the now acknowledged 'sent' data
		_, err = txn.Exec(`DELETE FROM syncv3_device_list_updates WHERE user_id=$1 AND device_id=$2 AND bucket=$3`, userID, deviceID, BucketSent)
		if err != nil {
			return err
		}
		// grab any 'new' updates
		result, err = t.selectDeviceListChangesInBucket(txn, userID, deviceID, BucketNew)
		if err != nil {
			return err
		}

		// mark these 'new' updates as 'sent'
		_, err = txn.Exec(`UPDATE syncv3_device_list_updates SET bucket=$1 WHERE user_id=$2 AND device_id=$3 AND bucket=$4`, BucketSent, userID, deviceID, BucketNew)
		return err
	})
	return
}

func (t *DeviceListTable) selectDeviceListChangesInBucket(txn *sqlx.Tx, userID, deviceID string, bucket int) (result internal.MapStringInt, err error) {
	rows, err := txn.Query(`SELECT target_user_id, target_state FROM syncv3_device_list_updates WHERE user_id=$1 AND device_id=$2 AND bucket=$3`, userID, deviceID, bucket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result = make(internal.MapStringInt)
	var targetUserID string
	var targetState int
	for rows.Next() {
		if err := rows.Scan(&targetUserID, &targetState); err != nil {
			return nil, err
		}
		result[targetUserID] = targetState
	}
	return result, rows.Err()
}
