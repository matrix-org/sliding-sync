package internal

import (
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
)

// MigrateDeviceIDs atomically updates a single device ID across all tables holding
// device IDs. If any error occurs, the transaction is aborted and no updates are
// committed.
func MigrateDeviceIDs(db *sqlx.DB, oldDeviceID, newDeviceID string) error {
	return sqlutil.WithTransaction(db, func(txn *sqlx.Tx) (dbErr error) {
		dbErr = migrateDeviceID(txn, "syncv3_sync2_devices", "device_id", oldDeviceID, newDeviceID, true)
		if dbErr != nil {
			return
		}
		dbErr = migrateDeviceID(txn, "syncv3_device_data", "device_id", oldDeviceID, newDeviceID, true)
		if dbErr != nil {
			return
		}
		dbErr = migrateDeviceID(txn, "syncv3_to_device_messages", "device_id", oldDeviceID, newDeviceID, false)
		if dbErr != nil {
			return
		}
		dbErr = migrateDeviceID(txn, "syncv3_to_device_ack_pos", "device_id", oldDeviceID, newDeviceID, true)
		if dbErr != nil {
			return
		}
		// "user_id" here is not a bug; the column is poorly named.
		dbErr = migrateDeviceID(txn, "syncv3_txns", "user_id", oldDeviceID, newDeviceID, false)
		if dbErr != nil {
			return
		}
		return
	})
}

// migrateDeviceID updates the device_id field for a single row in a single table.
// It should be used only to migrate from the old device_id format to the new.
//
// If singleRow is true, we check that we updated exactly one row. Otherwise we check
// that we updated at least one row.
func migrateDeviceID(txn *sqlx.Tx, table, column, oldDeviceID, newDeviceID string, singleRow bool) error {
	res, err := txn.Exec(`UPDATE $1 SET $2 = $4 WHERE $2 = $3`, table, column, oldDeviceID, newDeviceID)
	if err != nil {
		return err
	}
	rowsAffected, err := res.RowsAffected()
	if singleRow && rowsAffected != 1 {
		return fmt.Errorf(
			"migrateDeviceID(%s): expected %s -> %s to update 1 row, but actually updated %d rows",
			table, oldDeviceID, newDeviceID, rowsAffected,
		)
	}
	if !singleRow && rowsAffected == 0 {
		return fmt.Errorf(
			"migrateDeviceID(%s): expected %s -> %s to update at least 1 row, but actually updated 0 rows",
			table, oldDeviceID, newDeviceID,
		)
	}
	return nil
}
