package sync2

import (
	"crypto/sha256"
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
)

func MigrateDeviceIDs(db *sqlx.DB, secret string, whoamiClient Client, commit bool) error {
	return sqlutil.WithTransaction(db, func(txn *sqlx.Tx) (err error) {
		migrated, err := isMigrated(txn)
		if err != nil {
			return
		}
		if migrated {
			logger.Debug().Msg("MigrateDeviceIDs: migration has already taken place.")
		}

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

func isMigrated(txn *sqlx.Tx) (migrated bool, err error) {
	err = txn.QueryRow(`
		SELECT EXISTS(
		    SELECT 1 FROM information_schema.columns
			WHERE table_name = 'syncv3_sync2_devices' AND column_name = 'user_id'
		);
	`).Scan(&migrated)

	if err != nil {
		err = fmt.Errorf("checkNotMigrated failed: %s", err)
	}
	return
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
		DROP CONSTRAINT syncv3_to_device_messages_pkey,
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
		ADD COLUMN user_id TEXT;
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

func runMigration(txn *sqlx.Tx, secret string, whoamiClient Client) (err error) {
	logger.Info().Msg("Loading old-style devices into memory")
	var devices []oldDevice
	err = txn.Select(
		&devices,
		`SELECT device_id, user_id, v2_token_encrypted, since FROM syncv3_sync2_devices;`,
	)
	if err != nil {
		return
	}

	logger.Info().Msgf("Got %d devices to migrate", len(devices))

	hasher := sha256.New()
	hasher.Write([]byte(secret))
	key := hasher.Sum(nil)

	// TODO: can we get away with doing this sequentially, or should we parallelise
	//       this like the poller startup routine does?
	for i, device := range devices {
		device.AccessToken, err = decrypt(device.AccessTokenEncrypted, key)
		if err != nil {
			return
		}
		logger.Info().Msgf("%4d/%4d migrating device %s", i+1, len(devices), device.AccessTokenHash)
		err = migrateDevice(txn, whoamiClient, &device)
		if err != nil {
			return
		}
	}

	return
}

func migrateDevice(txn *sqlx.Tx, whoamiClient Client, device *oldDevice) (err error) {
	gotUserID, gotDeviceID, err := whoamiClient.WhoAmI(device.AccessToken)
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
func expectAtMostOneRowAffected(ra int64) bool    { return ra == 0 || ra == 1 }

func finish(txn *sqlx.Tx) (err error) {
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
		ALTER COLUMN user_id SET NOT NULL,
		ADD PRIMARY KEY (user_id, device_id);
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
