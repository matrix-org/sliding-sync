package state

import (
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/sqlutil"
)

// ToDeviceTable stores to_device messages for devices.
type ToDeviceTable struct{}

type ToDeviceRow struct {
	Position int64  `db:"position"`
	DeviceID string `db:"device_id"`
	Message  string `db:"message"`
}

type ToDeviceRowChunker []ToDeviceRow

func (c ToDeviceRowChunker) Len() int {
	return len(c)
}
func (c ToDeviceRowChunker) Subslice(i, j int) sqlutil.Chunker {
	return c[i:j]
}

func NewToDeviceTable(db *sqlx.DB) *ToDeviceTable {
	// make sure tables are made
	db.MustExec(`
	CREATE SEQUENCE IF NOT EXISTS syncv3_to_device_messages_seq;
	CREATE TABLE IF NOT EXISTS syncv3_to_device_messages (
		position BIGINT NOT NULL PRIMARY KEY DEFAULT nextval('syncv3_to_device_messages_seq'),
		device_id TEXT NOT NULL,
		message TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS syncv3_to_device_messages_device_idx ON syncv3_to_device_messages(device_id);
	`)
	return &ToDeviceTable{}
}

func (t *ToDeviceTable) Messages(txn *sqlx.Tx, deviceID string, from int64) (msgs []gomatrixserverlib.SendToDeviceEvent, to int64, err error) {
	var rows []ToDeviceRow
	err = txn.Select(&rows, `SELECT position, message FROM syncv3_to_device_messages WHERE device_id = $1 AND position > $2 ORDER BY position ASC`, deviceID, from)
	if len(rows) == 0 {
		to = from
		return
	}
	to = rows[len(rows)-1].Position
	msgs = make([]gomatrixserverlib.SendToDeviceEvent, len(rows))
	for i := range rows {
		var stdev gomatrixserverlib.SendToDeviceEvent
		if err = json.Unmarshal([]byte(rows[i].Message), &stdev); err != nil {
			return
		}
		msgs[i] = stdev
	}
	return
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
	chunks := sqlutil.Chunkify(2, 65535, ToDeviceRowChunker(rows))
	for _, chunk := range chunks {
		_, err := txn.NamedExec(`INSERT INTO syncv3_to_device_messages (device_id, message)
        VALUES (:device_id, :message)`, chunk)
		if err != nil {
			return err
		}
	}
	return nil
}
