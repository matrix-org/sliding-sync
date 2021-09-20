package sync2

import (
	"os"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/matrix-org/sync-v3/sqlutil"
	"github.com/rs/zerolog"
)

var log = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

type Device struct {
	UserID   string `db:"user_id"`
	DeviceID string `db:"device_id"`
	Since    string `db:"since"`
}

// Storage remembers sync v2 tokens per-device
type Storage struct {
	db *sqlx.DB
}

func NewStore(postgresURI string) *Storage {
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		log.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_sync2_devices (
		device_id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL, -- populated from /whoami
		since TEXT NOT NULL
	);`)

	return &Storage{
		db: db,
	}
}

func (s *Storage) Device(deviceID string) (*Device, error) {
	var d Device
	err := s.db.Get(&d, `SELECT device_id, user_id, since FROM syncv3_sync2_devices WHERE device_id=$1`, deviceID)
	return &d, err
}

func (s *Storage) InsertDevice(deviceID string) (*Device, error) {
	var device Device
	err := sqlutil.WithTransaction(s.db, func(txn *sqlx.Tx) error {
		// make sure there is a device entry for this device ID. If one already exists, don't clobber
		// the since value else we'll forget our position!
		result, err := txn.Exec(`
			INSERT INTO syncv3_sync2_devices(device_id, since, user_id) VALUES($1,$2,$3)
			ON CONFLICT (device_id) DO NOTHING`,
			deviceID, "", "",
		)
		if err != nil {
			return err
		}
		device.DeviceID = deviceID

		// if we inserted a row that means it's a brand new device ergo there is no since token
		if ra, err := result.RowsAffected(); err == nil && ra == 1 {
			return nil
		}

		// Return the since value as we may start a new poller with this session.
		return txn.QueryRow("SELECT since, user_id FROM syncv3_sync2_devices WHERE device_id = $1", deviceID).Scan(&device.Since, &device.UserID)
	})
	return &device, err
}

func (s *Storage) UpdateDeviceSince(deviceID, since string) error {
	_, err := s.db.Exec(`UPDATE syncv3_sync2_devices SET since = $1 WHERE device_id = $2`, since, deviceID)
	return err
}

func (s *Storage) UpdateUserIDForDevice(deviceID, userID string) error {
	_, err := s.db.Exec(`UPDATE syncv3_sync2_devices SET user_id = $1 WHERE device_id = $2`, userID, deviceID)
	return err
}
