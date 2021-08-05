package state

import (
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/sqlutil"
)

// ToDeviceTable stores to_device messages for devices.
type ToDeviceTable struct {
	db *sqlx.DB
}

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
	return &ToDeviceTable{db}
}

func (t *ToDeviceTable) Messages(deviceID string, from, to, limit int64) (msgs []json.RawMessage, upTo int64, err error) {
	upTo = to
	var rows []ToDeviceRow
	err = t.db.Select(&rows,
		`SELECT position, message FROM syncv3_to_device_messages WHERE device_id = $1 AND position > $2 AND position <= $3 ORDER BY position ASC LIMIT $4`,
		deviceID, from, to, limit,
	)
	if len(rows) == 0 {
		return
	}
	msgs = make([]json.RawMessage, len(rows))
	for i := range rows {
		msgs[i] = json.RawMessage(rows[i].Message)
	}
	// if a limit was applied, we may not get up to 'to'
	upTo = rows[len(rows)-1].Position
	return
}

func (t *ToDeviceTable) InsertMessages(deviceID string, msgs []gomatrixserverlib.SendToDeviceEvent) (pos int64, err error) {
	var lastPos int64
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
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
			result, err := t.db.NamedQuery(`INSERT INTO syncv3_to_device_messages (device_id, message)
        VALUES (:device_id, :message) RETURNING position`, chunk)
			if err != nil {
				return err
			}
			for result.Next() {
				if err = result.Scan(&lastPos); err != nil {
					return err
				}
			}
			result.Close()
		}
		return nil
	})
	return lastPos, err
}
