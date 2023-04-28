package sync2

import (
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"os"
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

// DevicesTable remembers syncv2 since positions per-device
type DevicesTable struct {
	db *sqlx.DB
}

func NewDevicesTable(db *sqlx.DB, secret string) *DevicesTable {
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_sync2_devices (
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		PRIMARY KEY (user_id, device_id),
		since TEXT NOT NULL
	);`)

	return &DevicesTable{
		db: db,
	}
}

func (s *DevicesTable) RemoveDevice(deviceID string) error {
	_, err := s.db.Exec(
		`DELETE FROM syncv3_sync2_devices WHERE device_id = $1`, deviceID,
	)
	log.Info().Str("device", deviceID).Msg("Deleting device")
	return err
}

// InsertDevice creates a new devices row with a blank since token if no such row
// exists. Otherwise, it does nothing.
func (s *DevicesTable) InsertDevice(userID, deviceID string) error {
	_, err := s.db.Exec(
		` INSERT INTO syncv3_sync2_devices(user_id, device_id, since) VALUES($1,$2,$3)
		ON CONFLICT (user_id, device_id) DO NOTHING`,
		userID, deviceID, "",
	)
	return err
}

func (s *DevicesTable) UpdateDeviceSince(deviceID, since string) error {
	_, err := s.db.Exec(`UPDATE syncv3_sync2_devices SET since = $1 WHERE device_id = $2`, since, deviceID)
	return err
}
