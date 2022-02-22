package state

import (
	"database/sql"
	"encoding/json"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sync-v3/sqlutil"
	"github.com/tidwall/gjson"
)

// ToDeviceTable stores to_device messages for devices.
type ToDeviceTable struct {
	db        *sqlx.DB
	latestPos int64
}

type ToDeviceRow struct {
	Position int64  `db:"position"`
	DeviceID string `db:"device_id"`
	Message  string `db:"message"`
	Type     string `db:"event_type"`
	Sender   string `db:"sender"`
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
		event_type TEXT NOT NULL,
		sender TEXT NOT NULL,
		message TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS syncv3_to_device_messages_device_idx ON syncv3_to_device_messages(device_id);
	`)
	var latestPos int64
	if err := db.QueryRow(`SELECT coalesce(MAX(position),0) FROM syncv3_to_device_messages`).Scan(&latestPos); err != nil && err != sql.ErrNoRows {
		panic(err)
	}
	return &ToDeviceTable{db, latestPos}
}

func (t *ToDeviceTable) DeleteMessagesUpToAndIncluding(deviceID string, toIncl int64) error {
	_, err := t.db.Exec(`DELETE FROM syncv3_to_device_messages WHERE device_id = $1 AND position <= $2`, deviceID, toIncl)
	return err
}

// Query to-device messages for this device, exclusive of from and inclusive of to. If a to value is unknown, use -1.
func (t *ToDeviceTable) Messages(deviceID string, from, to, limit int64) (msgs []json.RawMessage, upTo int64, err error) {
	if to == -1 {
		to = t.latestPos
	}
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

func (t *ToDeviceTable) InsertMessages(deviceID string, msgs []json.RawMessage) (pos int64, err error) {
	var lastPos int64
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		rows := make([]ToDeviceRow, len(msgs))
		for i := range msgs {
			m := gjson.ParseBytes(msgs[i])
			rows[i] = ToDeviceRow{
				DeviceID: deviceID,
				Message:  string(msgs[i]),
				Type:     m.Get("type").Str,
				Sender:   m.Get("sender").Str,
			}
		}

		chunks := sqlutil.Chunkify(4, MaxPostgresParameters, ToDeviceRowChunker(rows))
		for _, chunk := range chunks {
			result, err := t.db.NamedQuery(`INSERT INTO syncv3_to_device_messages (device_id, message, event_type, sender)
        VALUES (:device_id, :message, :event_type, :sender) RETURNING position`, chunk)
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
	if lastPos > t.latestPos {
		t.latestPos = lastPos
	}
	return lastPos, err
}
