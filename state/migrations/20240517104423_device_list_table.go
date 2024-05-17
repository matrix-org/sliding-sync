package migrations

import (
	"context"
	"database/sql"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/lib/pq"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/pressly/goose/v3"
)

type OldDeviceData struct {
	// Contains the latest device_one_time_keys_count values.
	// Set whenever this field arrives down the v2 poller, and it replaces what was previously there.
	OTKCounts internal.MapStringInt `json:"otk"`
	// Contains the latest device_unused_fallback_key_types value
	// Set whenever this field arrives down the v2 poller, and it replaces what was previously there.
	// If this is a nil slice this means no change. If this is an empty slice then this means the fallback key was used up.
	FallbackKeyTypes []string `json:"fallback"`

	DeviceLists OldDeviceLists `json:"dl"`

	// bitset for which device data changes are present. They accumulate until they get swapped over
	// when they get reset
	ChangedBits int `json:"c"`

	UserID   string
	DeviceID string
}

type OldDeviceLists struct {
	// map user_id -> DeviceList enum
	New  internal.MapStringInt `json:"n"`
	Sent internal.MapStringInt `json:"s"`
}

func init() {
	goose.AddMigrationContext(upDeviceListTable, downDeviceListTable)
}

func upDeviceListTable(ctx context.Context, tx *sql.Tx) error {
	// create the table. It's a bit gross we need to dupe the schema here, but this is the first migration to
	// add a new table like this.
	_, err := tx.Exec(`
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
	if err != nil {
		return err
	}

	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM syncv3_device_data`).Scan(&count); err != nil {
		return err
	}
	logger.Info().Int("count", count).Msg("transferring device list data for devices")

	// scan for existing CBOR (streaming as the CBOR can be large) and for each row:
	rows, err := tx.Query(`SELECT user_id, device_id, data FROM syncv3_device_data`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var userID string
	var deviceID string
	var data []byte
	// every N seconds log an update
	updateFrequency := time.Second * 2
	lastUpdate := time.Now()
	i := 0
	for rows.Next() {
		i++
		if time.Since(lastUpdate) > updateFrequency {
			logger.Info().Msgf("%d/%d process device list data", i, count)
			lastUpdate = time.Now()
		}
		//  * deserialise the CBOR
		if err := rows.Scan(&userID, &deviceID, &data); err != nil {
			return err
		}
		result, err := deserialiseCBOR(data)
		if err != nil {
			return err
		}

		//  * transfer the device lists to the new device lists table
		// uses a bulk copy that lib/pq supports
		stmt, err := tx.Prepare(pq.CopyIn("syncv3_device_list_updates", "user_id", "device_id", "target_user_id", "target_state", "bucket"))
		if err != nil {
			return err
		}
		for targetUser, targetState := range result.DeviceLists.New {
			if _, err := stmt.Exec(userID, deviceID, targetUser, targetState, state.BucketNew); err != nil {
				return err
			}
		}
		for targetUser, targetState := range result.DeviceLists.Sent {
			if _, err := stmt.Exec(userID, deviceID, targetUser, targetState, state.BucketSent); err != nil {
				return err
			}
		}
		if _, err = stmt.Exec(); err != nil {
			return err
		}
		if err = stmt.Close(); err != nil {
			return err
		}

		//  * delete the device lists from the CBOR and update
		result.DeviceLists = OldDeviceLists{
			New:  make(internal.MapStringInt),
			Sent: make(internal.MapStringInt),
		}
		data, err := cbor.Marshal(result)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE syncv3_device_data SET data=$1 WHERE user_id=$2 AND device_id=$3`, data, userID, deviceID)
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

func downDeviceListTable(ctx context.Context, tx *sql.Tx) error {
	// no-op: we'll drop the device list updates but still work correctly as new/sent are still in the cbor but are empty
	return nil
}

func deserialiseCBOR(data []byte) (*OldDeviceData, error) {
	opts := cbor.DecOptions{
		MaxMapPairs: 1000000000, // 1 billion :(
	}
	decMode, err := opts.DecMode()
	if err != nil {
		return nil, err
	}
	var result *OldDeviceData
	if err = decMode.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}
