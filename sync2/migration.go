package sync2

import (
	"crypto/sha256"
	"fmt"
	"github.com/getsentry/sentry-go"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"net/http"
	"time"
)

// MigrateDeviceIDs performs a one-off DB migration from the old device ids (hash of
// access token) to the new device ids (actual device ids from the homeserver). This is
// not backwards compatible. If the migration has already taken place, this function is
// a no-op.
//
// This code will be removed in a future version of the proxy.
func MigrateDeviceIDs(destHomeserver, postgresURI, secret string, commit bool) error {
	whoamiClient := &HTTPClient{
		Client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		DestinationServer: destHomeserver,
	}
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		sentry.CaptureException(err)
		logger.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}

	// Ensure the new table exists.
	NewTokensTable(db, secret)

	return sqlutil.WithTransaction(db, func(txn *sqlx.Tx) (err error) {
		migrated, err := isMigrated(txn)
		if err != nil {
			return
		}
		if migrated {
			logger.Debug().Msg("MigrateDeviceIDs: migration has already taken place")
			return nil
		}
		logger.Info().Msgf("MigrateDeviceIDs: starting (commit=%t)", commit)

		start := time.Now()
		defer func() {
			elapsed := time.Since(start)
			logger.Debug().Msgf("MigrateDeviceIDs: took %s", elapsed)
		}()
		err = alterTables(txn)
		if err != nil {
			return
		}

		err = runMigration(txn, secret, whoamiClient)
		if err != nil {
			return
		}
		err = finish(txn)
		if err != nil {
			return
		}

		if !commit {
			err = fmt.Errorf("MigrateDeviceIDs: migration succeeded without errors, but commit is false - rolling back anyway")
		} else {
			logger.Info().Msg("MigrateDeviceIDs: migration succeeded - committing")
		}
		return
	})
}

func isMigrated(txn *sqlx.Tx) (bool, error) {
	// Keep this dead simple for now. This is a one-off migration, before version 1.0.
	// In the future we'll rip this out and tell people that it's their job to ensure
	// this migration has run before they upgrade beyond the rip-out point.

	// We're going to detect if the migration has run by testing for the existence of
	// a column added by the migration. First, check that the table exists.
	var tableExists bool
	err := txn.QueryRow(`
		SELECT EXISTS(
		    SELECT 1 FROM information_schema.columns
			WHERE table_name = 'syncv3_txns'
		);
	`).Scan(&tableExists)
	if err != nil {
		return false, fmt.Errorf("isMigrated: %s", err)
	}
	if !tableExists {
		// The proxy has never been run before and its tables have never been created.
		// We do not need to run the migration.
		logger.Debug().Msg("isMigrated: no syncv3_txns table, no migration needed")
		return true, nil
	}

	var migrated bool
	err = txn.QueryRow(`
		SELECT EXISTS(
		    SELECT 1 FROM information_schema.columns
			WHERE table_name = 'syncv3_txns' AND column_name = 'device_id'
		);
	`).Scan(&migrated)

	if err != nil {
		return false, fmt.Errorf("isMigrated: %s", err)
	}
	return migrated, nil
}

func alterTables(txn *sqlx.Tx) (err error) {
	_, err = txn.Exec(`
		ALTER TABLE syncv3_sync2_devices
		DROP CONSTRAINT syncv3_sync2_devices_pkey;
	`)
	if err != nil {
		return
	}

	_, err = txn.Exec(`
		ALTER TABLE syncv3_to_device_messages
		ADD COLUMN user_id TEXT;
	`)
	if err != nil {
		return
	}

	_, err = txn.Exec(`
		ALTER TABLE syncv3_to_device_ack_pos
		DROP CONSTRAINT syncv3_to_device_ack_pos_pkey,
		ADD COLUMN user_id TEXT;
	`)
	if err != nil {
		return
	}

	_, err = txn.Exec(`
		ALTER TABLE syncv3_txns
		DROP CONSTRAINT syncv3_txns_user_id_event_id_key,
		ADD COLUMN device_id TEXT;
	`)

	return
}

type oldDevice struct {
	AccessToken          string // not a DB row, but it's convenient to write to here
	AccessTokenHash      string `db:"device_id"`
	UserID               string `db:"user_id"`
	AccessTokenEncrypted string `db:"v2_token_encrypted"`
	Since                string `db:"since"`
}

func runMigration(txn *sqlx.Tx, secret string, whoamiClient Client) error {
	logger.Info().Msg("Loading old-style devices into memory")
	var devices []oldDevice
	err := txn.Select(
		&devices,
		`SELECT device_id, user_id, v2_token_encrypted, since FROM syncv3_sync2_devices;`,
	)
	if err != nil {
		return fmt.Errorf("runMigration: failed to select devices: %s", err)
	}

	logger.Info().Msgf("Got %d devices to migrate", len(devices))

	hasher := sha256.New()
	hasher.Write([]byte(secret))
	key := hasher.Sum(nil)

	// This migration runs sequentially, one device at a time. We have found this to be
	// quick enough in practice.
	numErrors := 0
	for i, device := range devices {
		device.AccessToken, err = decrypt(device.AccessTokenEncrypted, key)
		if err != nil {
			return fmt.Errorf("runMigration: failed to decrypt device: %s", err)
		}
		logger.Info().Msgf("%4d/%4d migrating device %s", i+1, len(devices), device.AccessTokenHash)
		err = migrateDevice(txn, whoamiClient, &device)
		if err != nil {
			logger.Err(err).Msgf("runMigration: failed to migrate device %s", device.AccessTokenHash)
			numErrors++
		}
	}
	if numErrors > 0 {
		return fmt.Errorf("runMigration: there were %d failures", numErrors)
	}

	return nil
}

func migrateDevice(txn *sqlx.Tx, whoamiClient Client, device *oldDevice) (err error) {
	gotUserID, gotDeviceID, err := whoamiClient.WhoAmI(device.AccessToken)
	if err == HTTP401 {
		logger.Warn().Msgf(
			"migrateDevice: access token for %s %s has expired. Dropping device and metadata.",
			device.UserID, device.AccessTokenHash,
		)
		return cleanupDevice(txn, device)
	}
	if err != nil {
		return err
	}
	// Sanity check the user ID from the HS matches our records
	if gotUserID != device.UserID {
		return fmt.Errorf(
			"/whoami response was for the wrong user. Queried for %s, but got response for %s",
			device.UserID, gotUserID,
		)
	}

	err = exec(
		txn,
		`INSERT INTO syncv3_sync2_tokens(token_hash, token_encrypted, user_id, device_id, last_seen)
VALUES ($1, $2, $3, $4, $5)`,
		expectOneRowAffected,
		device.AccessTokenHash, device.AccessTokenEncrypted, gotDeviceID, gotDeviceID, time.Now(),
	)
	if err != nil {
		return
	}

	// For these first four tables:
	// - use the actual device ID instead of the access token hash, and
	// - ensure a user ID is set.
	err = exec(
		txn,
		`UPDATE syncv3_sync2_devices SET user_id = $1, device_id = $2 WHERE device_id = $3`,
		expectOneRowAffected,
		gotUserID, gotDeviceID, device.AccessTokenHash,
	)
	if err != nil {
		return
	}

	err = exec(
		txn,
		`UPDATE syncv3_to_device_messages SET user_id = $1, device_id = $2 WHERE device_id = $3`,
		expectAnyNumberOfRowsAffected,
		gotUserID, gotDeviceID, device.AccessTokenHash,
	)
	if err != nil {
		return
	}

	err = exec(
		txn,
		`UPDATE syncv3_to_device_ack_pos SET user_id = $1, device_id = $2 WHERE device_id = $3`,
		expectAtMostOneRowAffected,
		gotUserID, gotDeviceID, device.AccessTokenHash,
	)
	if err != nil {
		return
	}

	err = exec(
		txn,
		`UPDATE syncv3_device_data SET user_id = $1, device_id = $2 WHERE device_id = $3`,
		expectAtMostOneRowAffected,
		gotUserID, gotDeviceID, device.AccessTokenHash,
	)
	if err != nil {
		return
	}

	// Confusingly, the txns table used to store access token hashes under the user_id
	// column. Write the actual user ID to the user_id column, and the actual device ID
	// to the device_id column.
	err = exec(
		txn,
		`UPDATE syncv3_txns SET user_id = $1, device_id = $2 WHERE user_id = $3`,
		expectAtMostOneRowAffected,
		gotUserID, gotDeviceID, device.AccessTokenHash,
	)
	if err != nil {
		return
	}

	return
}

func cleanupDevice(txn *sqlx.Tx, device *oldDevice) (err error) {
	// The homeserver does not recognise this access token. Because we have no
	// record of the device_id from the homeserver, it will never be possible to
	// spot that a future refreshed access token belongs to the device we're
	// handling here. Therefore this device is not useful to the proxy.
	//
	// If we leave this device's rows in situ, we may end up with rows in
	// syncv3_to_device_messages, syncv3_to_device_ack_pos and syncv3_txns which have
	// null values for the new fields, which will mean we fail to impose the uniqueness
	// constraints at the end of the migration. Instead, drop those rows.
	err = exec(
		txn,
		`DELETE FROM syncv3_sync2_devices WHERE device_id = $1`,
		expectOneRowAffected,
		device.AccessTokenHash,
	)
	if err != nil {
		return
	}

	err = exec(
		txn,
		`DELETE FROM syncv3_to_device_messages WHERE device_id = $1`,
		expectAnyNumberOfRowsAffected,
		device.AccessTokenHash,
	)
	if err != nil {
		return
	}

	err = exec(
		txn,
		`DELETE FROM syncv3_to_device_ack_pos WHERE device_id = $1`,
		expectAtMostOneRowAffected,
		device.AccessTokenHash,
	)
	if err != nil {
		return
	}

	err = exec(
		txn,
		`DELETE FROM syncv3_device_data WHERE device_id = $1`,
		expectAtMostOneRowAffected,
		device.AccessTokenHash,
	)
	if err != nil {
		return
	}

	err = exec(
		txn,
		`DELETE FROM syncv3_txns WHERE user_id = $1`,
		expectAnyNumberOfRowsAffected,
		device.AccessTokenHash,
	)
	if err != nil {
		return
	}

	return
}

func exec(txn *sqlx.Tx, query string, checkRowsAffected func(ra int64) bool, args ...any) error {
	res, err := txn.Exec(query, args...)
	if err != nil {
		return err
	}
	ra, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if !checkRowsAffected(ra) {
		return fmt.Errorf("Failed checkRowsAffected: got %d", ra)
	}
	return nil
}

func expectOneRowAffected(ra int64) bool          { return ra == 1 }
func expectAnyNumberOfRowsAffected(ra int64) bool { return true }
func logRowsAffected(msg string) func(ra int64) bool {
	return func(ra int64) bool {
		logger.Info().Msgf(msg, ra)
		return true
	}
}
func expectAtMostOneRowAffected(ra int64) bool { return ra == 0 || ra == 1 }

func finish(txn *sqlx.Tx) (err error) {
	// OnExpiredToken used to delete from the to-device table, but not from the
	// to-device ack pos table. Fix this up by deleting any orphaned ack pos rows.
	err = exec(
		txn,
		`
		DELETE FROM syncv3_to_device_ack_pos
		WHERE device_id IN (
		    SELECT syncv3_to_device_ack_pos.device_id
		    FROM syncv3_to_device_ack_pos LEFT JOIN syncv3_sync2_devices USING (device_id)
		);`,
		logRowsAffected("Deleted %d stale rows from syncv3_to_device_ack_pos"),
	)
	if err != nil {
		return
	}

	_, err = txn.Exec(`
		ALTER TABLE syncv3_sync2_devices
		DROP COLUMN v2_token_encrypted,
		ADD PRIMARY KEY (user_id, device_id);
	`)
	if err != nil {
		return
	}

	_, err = txn.Exec(`
		ALTER TABLE syncv3_to_device_messages
		ALTER COLUMN user_id SET NOT NULL;
	`)
	if err != nil {
		return
	}

	_, err = txn.Exec(`
		ALTER TABLE syncv3_to_device_ack_pos
		ALTER COLUMN user_id SET NOT NULL,
		ADD PRIMARY KEY (user_id, device_id);
	`)
	if err != nil {
		return
	}

	_, err = txn.Exec(`
		ALTER TABLE syncv3_txns
		ALTER COLUMN device_id SET NOT NULL,
		ADD UNIQUE(user_id, device_id, event_id);
	`)
	if err != nil {
		return
	}

	return
}
