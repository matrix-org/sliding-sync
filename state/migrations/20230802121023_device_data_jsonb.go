package migrations

import (
	"context"
	"database/sql"
	"errors"
	"strings"

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
	_, err = tx.ExecContext(ctx, "UPDATE syncv3_device_data SET dataj = encode(data, 'escape')::JSONB;")
	if err != nil {
		return err
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
