package state

import (
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/gomatrixserverlib"
)

// ToDeviceTable stores to_device messages for devices.
type ToDeviceTable struct{}

type ToDeviceRow struct {
	DeviceID string `db:"device_id"`
	Message  string `db:"message"`
}

func NewToDeviceTable(db *sqlx.DB) *RoomsTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_to_device_messages (
		device_id TEXT NOT NULL PRIMARY KEY,
		message TEXT NOT NULL
	);
	`)
	return &RoomsTable{}
}

func (t *ToDeviceTable) InsertMessages(txn *sqlx.Tx, deviceID string, msgs []gomatrixserverlib.SendToDeviceEvent) (err error) {
	rows := make([]ToDeviceRow, len(msgs))
	for i := range msgs {
		msgJSON, err := json.Marshal(msgs[i])
		if err != nil {
			return fmt.Errorf("InsertMessages: failed to marshal to_device event: %s", err)
		}
		rows[i] = ToDeviceRow{
			DeviceID: deviceID,
			Message:  string(msgJSON),
		}
	}
	return nil
}
