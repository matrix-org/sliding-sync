package migrations

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upJSONB, downJSONB)
}

func upJSONB(ctx context.Context, tx *sql.Tx) error {
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
	if strings.ToLower(dataType) == "jsonb" {
		return nil
	}

	_, err = tx.ExecContext(ctx, "ALTER TABLE IF EXISTS syncv3_device_data ADD COLUMN IF NOT EXISTS dataj JSONB;")
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

	for dd, d := range deviceDatas {
		_, err = tx.ExecContext(ctx, "UPDATE syncv3_device_data SET dataj = $1 WHERE user_id = $2 AND device_id = $3;", d, dd.UserID, dd.DeviceID)
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
	_, err = tx.ExecContext(ctx, "ALTER TABLE IF EXISTS syncv3_device_data RENAME COLUMN dataj TO data;")
	if err != nil {
		return err
	}
	return nil
}

func downJSONB(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "ALTER TABLE IF EXISTS syncv3_device_data ADD COLUMN IF NOT EXISTS datab BYTEA;")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE syncv3_device_data SET datab = (data::TEXT)::BYTEA;")
	if err != nil {
		return err
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
