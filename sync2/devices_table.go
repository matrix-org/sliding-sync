package sync2

import (
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

type Device struct {
	UserID   string `db:"user_id"`
	DeviceID string `db:"device_id"`
	Since    string `db:"since"`
}

// DevicesTable remembers syncv2 since positions per-device
type DevicesTable struct {
	db *sqlx.DB
}

func NewDevicesTable(db *sqlx.DB) *DevicesTable {
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

// InsertDevice creates a new devices row with a blank since token if no such row
// exists. Otherwise, it does nothing.
func (t *DevicesTable) InsertDevice(txn *sqlx.Tx, userID, deviceID string) error {
	_, err := txn.Exec(
		` INSERT INTO syncv3_sync2_devices(user_id, device_id, since) VALUES($1,$2,$3)
		ON CONFLICT (user_id, device_id) DO NOTHING`,
		userID, deviceID, "",
	)
	return err
}

func (t *DevicesTable) UpdateDeviceSince(userID, deviceID, since string) error {
	_, err := t.db.Exec(`UPDATE syncv3_sync2_devices SET since = $1 WHERE user_id = $2 AND device_id = $3`, since, userID, deviceID)
	return err
}
