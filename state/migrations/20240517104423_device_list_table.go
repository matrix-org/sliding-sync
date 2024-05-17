package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sqlutil"
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
	);`)
	if err != nil {
		return err
	}

	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM syncv3_device_data`).Scan(&count); err != nil {
		return err
	}
	logger.Info().Int("count", count).Msg("transferring device list data for devices")

	// scan for existing CBOR (streaming as the CBOR with cursors as it can be large)
	_, err = tx.Exec(`DECLARE device_data_migration_cursor CURSOR FOR SELECT user_id, device_id, data FROM syncv3_device_data`)
	if err != nil {
		return err
	}
	defer tx.Exec("CLOSE device_data_migration_cursor")
	var userID string
	var deviceID string
	var data []byte
	// every N seconds log an update
	updateFrequency := time.Second * 2
	lastUpdate := time.Now()
	i := 0
	for {
		// logging
		i++
		if time.Since(lastUpdate) > updateFrequency {
			logger.Info().Msgf("%d/%d process device list data", i, count)
			lastUpdate = time.Now()
		}

		if err := tx.QueryRow(
			`FETCH NEXT FROM device_data_migration_cursor`,
		).Scan(&userID, &deviceID, &data); err != nil {
			if err == sql.ErrNoRows {
				// End of rows.
				break
			}
			return err
		}

		//  * deserialise the CBOR
		result, err := deserialiseCBOR(data)
		if err != nil {
			return err
		}

		//  * transfer the device lists to the new device lists table
		var deviceListRows []state.DeviceListRow
		for targetUser, targetState := range result.DeviceLists.New {
			deviceListRows = append(deviceListRows, state.DeviceListRow{
				UserID:       userID,
				DeviceID:     deviceID,
				TargetUserID: targetUser,
				TargetState:  targetState,
				Bucket:       state.BucketNew,
			})
		}
		for targetUser, targetState := range result.DeviceLists.Sent {
			deviceListRows = append(deviceListRows, state.DeviceListRow{
				UserID:       userID,
				DeviceID:     deviceID,
				TargetUserID: targetUser,
				TargetState:  targetState,
				Bucket:       state.BucketSent,
			})
		}
		chunks := sqlutil.Chunkify(5, state.MaxPostgresParameters, state.DeviceListChunker(deviceListRows))
		for _, chunk := range chunks {
			var placeholders []string
			var vals []interface{}
			listChunk := chunk.(state.DeviceListChunker)
			for i, deviceListRow := range listChunk {
				placeholders = append(placeholders, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d)",
					i*5+1,
					i*5+2,
					i*5+3,
					i*5+4,
					i*5+5,
				))
				vals = append(vals, deviceListRow.UserID, deviceListRow.DeviceID, deviceListRow.TargetUserID, deviceListRow.TargetState, deviceListRow.Bucket)
			}
			query := fmt.Sprintf(
				`INSERT INTO syncv3_device_list_updates(user_id, device_id, target_user_id, target_state, bucket) VALUES %s`,
				strings.Join(placeholders, ","),
			)
			_, err = tx.ExecContext(ctx, query, vals...)
			if err != nil {
				return fmt.Errorf("failed to bulk insert: %s", err)
			}
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
		_, err = tx.ExecContext(ctx, `UPDATE syncv3_device_data SET data=$1 WHERE user_id=$2 AND device_id=$3`, data, userID, deviceID)
		if err != nil {
			return err
		}
	}
	return nil
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
