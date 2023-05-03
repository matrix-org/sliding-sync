package sync2

import (
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
)

func MigrateDeviceIDs(db *sqlx.DB, whoamiClient Client, commit bool) error {
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

		err = runMigration(txn, whoamiClient)
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
	AccessToken          string
	AccessTokenHash      string `db:"device_id"`
	UserID               string `db:"user_id"`
	AccessTokenEncrypted string `db:"v2_token_encrypted"`
	Since                string `db:"since"`
}

func runMigration(txn *sqlx.Tx, whoamiClient Client) (err error) {

	logger.Info().Msg("Loading old-style devices into memory")
	var devices []oldDevice
	err = txn.Select(
		&devices,
		`SELECT device_id, user_id, v2_token_encrypted, since FROM syncv3_sync2_devices;`,
	)
	if err != nil {
		return
	}

	logger.Info().Msgf("Got %s devices to migrate", len(devices))
	for _, device := range devices {
		err = migrateDevice(txn, whoamiClient, &device)
		if err != nil {
			return
		}
	}

	return
}

func migrateDevice(txn *sqlx.Tx, whoamiClient Client, device *oldDevice) (err error) {
	// Migration is required. Fetch the "real" device ID.
	gotUserID, gotHSDeviceID, err := whoamiClient.WhoAmI(device.)
	if err != nil {
		return err
	}
	// Sanity check the user ID from the HS matches our records
	if gotUserID != d.UserID {
		return fmt.Errorf(
			"/whoami response was for the wrong user. Queried for %s, but got response for %s",
			d.UserID, gotUserID,
		)
	}
	return
}

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
