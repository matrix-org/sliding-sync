package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fxamacker/cbor/v2"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upCborDeviceData, downCborDeviceData)
}

func upCborDeviceData(ctx context.Context, tx *sql.Tx) error {
	// check if we even need to do anything
	var dataType string
	err := tx.QueryRow("select data_type from information_schema.columns where table_name = 'syncv3_device_data' AND column_name = 'data'").Scan(&dataType)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The table/column doesn't exist in is likely going to be created soon with the
			// correct schema
			return nil
		}
		return err
	}
	if strings.ToLower(dataType) == "bytea" {
		return nil
	}

	_, err = tx.ExecContext(ctx, "ALTER TABLE IF EXISTS syncv3_device_data ADD COLUMN IF NOT EXISTS datab BYTEA;")
	if err != nil {
		return err
	}

	rows, err := tx.Query("SELECT user_id, device_id, data FROM syncv3_device_data")
	if err != nil {
		return err
	}
	defer rows.Close()

	// abusing PollerID here
	var deviceData sync2.PollerID
	var data []byte

	// map from PollerID -> deviceData
	deviceDatas := make(map[sync2.PollerID][]byte)
	for rows.Next() {
		if err = rows.Scan(&deviceData.UserID, &deviceData.DeviceID, &data); err != nil {
			return err
		}
		deviceDatas[deviceData] = data
	}

	for dd, jsonBytes := range deviceDatas {
		var data internal.DeviceData
		if err := json.Unmarshal(jsonBytes, &data); err != nil {
			return fmt.Errorf("failed to unmarshal JSON: %v -> %v", string(jsonBytes), err)
		}
		cborBytes, err := cbor.Marshal(data)
		if err != nil {
			return fmt.Errorf("failed to marshal as CBOR: %v", err)
		}

		_, err = tx.ExecContext(ctx, "UPDATE syncv3_device_data SET datab = $1 WHERE user_id = $2 AND device_id = $3;", cborBytes, dd.UserID, dd.DeviceID)
		if err != nil {
			return err
		}
	}

	if rows.Err() != nil {
		return rows.Err()
	}

	_, err = tx.ExecContext(ctx, "ALTER TABLE IF EXISTS syncv3_device_data DROP COLUMN IF EXISTS data;")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "ALTER TABLE IF EXISTS syncv3_device_data RENAME COLUMN datab TO data;")
	if err != nil {
		return err
	}

	return nil
}

func downCborDeviceData(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "ALTER TABLE IF EXISTS syncv3_device_data ADD COLUMN IF NOT EXISTS dataj JSONB;")
	if err != nil {
		return err
	}
	rows, err := tx.Query("SELECT user_id, device_id, data FROM syncv3_device_data")
	if err != nil {
		return err
	}
	defer rows.Close()

	// abusing PollerID here
	var deviceData sync2.PollerID
	var data []byte

	// map from PollerID -> deviceData
	deviceDatas := make(map[sync2.PollerID][]byte)
	for rows.Next() {
		if err = rows.Scan(&deviceData.UserID, &deviceData.DeviceID, &data); err != nil {
			return err
		}
		deviceDatas[deviceData] = data
	}

	for dd, cborBytes := range deviceDatas {
		var data internal.DeviceData
		if err := cbor.Unmarshal(cborBytes, &data); err != nil {
			return fmt.Errorf("failed to unmarshal CBOR: %v", err)
		}
		jsonBytes, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("failed to marshal as JSON: %v", err)
		}

		_, err = tx.ExecContext(ctx, "UPDATE syncv3_device_data SET dataj = $1 WHERE user_id = $2 AND device_id = $3;", jsonBytes, dd.UserID, dd.DeviceID)
		if err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, "ALTER TABLE IF EXISTS syncv3_device_data DROP COLUMN IF EXISTS data;")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "ALTER TABLE IF EXISTS syncv3_device_data RENAME COLUMN dataj TO data;")
	if err != nil {
		return err
	}
	return nil
}
