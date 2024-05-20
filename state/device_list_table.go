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

type DeviceListRow struct {
	UserID       string `db:"user_id"`
	DeviceID     string `db:"device_id"`
	TargetUserID string `db:"target_user_id"`
	TargetState  int    `db:"target_state"`
	Bucket       int    `db:"bucket"`
}

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

func (t *DeviceListTable) Upsert(userID, deviceID string, deviceListChanges map[string]int) (err error) {
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		return t.UpsertTx(txn, userID, deviceID, deviceListChanges)
	})
	if err != nil {
		sentry.CaptureException(err)
	}
	return
}

// Upsert new device list changes.
func (t *DeviceListTable) UpsertTx(txn *sqlx.Tx, userID, deviceID string, deviceListChanges map[string]int) (err error) {
	if len(deviceListChanges) == 0 {
		return nil
	}
	var deviceListRows []DeviceListRow
	for targetUserID, targetState := range deviceListChanges {
		if targetState != internal.DeviceListChanged && targetState != internal.DeviceListLeft {
			sentry.CaptureException(fmt.Errorf("DeviceListTable.Upsert invalid target_state: %d this is a programming error", targetState))
			continue
		}
		deviceListRows = append(deviceListRows, DeviceListRow{
			UserID:       userID,
			DeviceID:     deviceID,
			TargetUserID: targetUserID,
			TargetState:  targetState,
			Bucket:       BucketNew,
		})
	}
	chunks := sqlutil.Chunkify(5, MaxPostgresParameters, DeviceListChunker(deviceListRows))
	for _, chunk := range chunks {
		_, err := txn.NamedExec(`
						INSERT INTO syncv3_device_list_updates(user_id, device_id, target_user_id, target_state, bucket)
						VALUES(:user_id, :device_id, :target_user_id, :target_state, :bucket)
						ON CONFLICT (user_id, device_id, target_user_id, bucket) DO UPDATE SET target_state = EXCLUDED.target_state`, chunk)
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *DeviceListTable) Select(userID, deviceID string, swap bool) (result internal.MapStringInt, err error) {
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		result, err = t.SelectTx(txn, userID, deviceID, swap)
		return err
	})
	return
}

// Select device list changes for this client. Returns a map of user_id => change enum.
func (t *DeviceListTable) SelectTx(txn *sqlx.Tx, userID, deviceID string, swap bool) (result internal.MapStringInt, err error) {
	if !swap {
		// read only view, just return what we previously sent and don't do anything else.
		return t.selectDeviceListChangesInBucket(txn, userID, deviceID, BucketSent)
	}

	// delete the now acknowledged 'sent' data
	_, err = txn.Exec(`DELETE FROM syncv3_device_list_updates WHERE user_id=$1 AND device_id=$2 AND bucket=$3`, userID, deviceID, BucketSent)
	if err != nil {
		return nil, err
	}
	// grab any 'new' updates and atomically mark these as 'sent'.
	// NB: we must not SELECT then UPDATE, because a 'new' row could be inserted after the SELECT and before the UPDATE, which
	// would then be incorrectly moved to 'sent' without being returned to the client, dropping the data. This happens because
	// the default transaction level is 'read committed', which /allows/ nonrepeatable reads which is:
	//  > A transaction re-reads data it has previously read and finds that data has been modified by another transaction (that committed since the initial read).
	// We could change the isolation level but this incurs extra performance costs in addition to serialisation errors which
	// need to be handled. It's easier to just use UPDATE .. RETURNING. Note that we don't require UPDATE .. RETURNING to be
	// atomic in any way, it's just that we need to guarantee each things SELECTed is also UPDATEd (so in the scenario above,
	// we don't care if the SELECT includes or excludes the 'new' row, but if it is SELECTed it MUST be UPDATEd).
	rows, err := txn.Query(`UPDATE syncv3_device_list_updates SET bucket=$1 WHERE user_id=$2 AND device_id=$3 AND bucket=$4 RETURNING target_user_id, target_state`, BucketSent, userID, deviceID, BucketNew)
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

type DeviceListChunker []DeviceListRow

func (c DeviceListChunker) Len() int {
	return len(c)
}
func (c DeviceListChunker) Subslice(i, j int) sqlutil.Chunker {
	return c[i:j]
}
