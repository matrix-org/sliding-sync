package state

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"github.com/tidwall/gjson"
)

const (
	ActionRequest = 1
	ActionCancel  = 2
)

// ToDeviceTable stores to_device messages for devices.
type ToDeviceTable struct {
	db *sqlx.DB
}

type ToDeviceRow struct {
	Position  int64   `db:"position"`
	UserID    string  `db:"user_id"`
	DeviceID  string  `db:"device_id"`
	Message   string  `db:"message"`
	Type      string  `db:"event_type"`
	Sender    string  `db:"sender"`
	UniqueKey *string `db:"unique_key"`
	Action    int     `db:"action"`
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
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		sender TEXT NOT NULL,
		message TEXT NOT NULL,
		-- nullable as these fields are not on all to-device events
		unique_key TEXT,
		action SMALLINT DEFAULT 0 -- 0 means unknown
	);
	CREATE TABLE IF NOT EXISTS syncv3_to_device_ack_pos (
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		PRIMARY KEY (user_id, device_id),
		unack_pos BIGINT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS syncv3_to_device_messages_device_idx ON syncv3_to_device_messages(device_id);
	CREATE INDEX IF NOT EXISTS syncv3_to_device_messages_ukey_idx ON syncv3_to_device_messages(unique_key, device_id);
	CREATE INDEX IF NOT EXISTS syncv3_to_device_messages_pos_device_idx ON syncv3_to_device_messages(position, device_id);
	`)
	return &ToDeviceTable{db}
}

func (t *ToDeviceTable) SetUnackedPosition(userID, deviceID string, pos int64) error {
	_, err := t.db.Exec(`INSERT INTO syncv3_to_device_ack_pos(user_id, device_id, unack_pos) VALUES($1,$2,$3) ON CONFLICT (user_id, device_id)
	DO UPDATE SET unack_pos=excluded.unack_pos`, userID, deviceID, pos)
	return err
}

func (t *ToDeviceTable) DeleteMessagesUpToAndIncluding(userID, deviceID string, toIncl int64) error {
	_, err := t.db.Exec(`DELETE FROM syncv3_to_device_messages WHERE user_id = $1 AND device_id = $2 AND position <= $3`, userID, deviceID, toIncl)
	return err
}

func (t *ToDeviceTable) DeleteAllMessagesForDevice(userID, deviceID string) error {
	// TODO: should these deletes take place in a transaction?
	_, err := t.db.Exec(`DELETE FROM syncv3_to_device_messages WHERE user_id = $1 AND device_id = $2`, userID, deviceID)
	if err != nil {
		return err
	}
	_, err = t.db.Exec(`DELETE FROM syncv3_to_device_ack_pos WHERE user_id = $1 AND device_id = $2`, userID, deviceID)
	return err
}

// Messages fetches up to `limit` to-device messages for this device, starting from and excluding `from`.
// Returns the fetches messages ordered by ascending position, as well as the position of the last to-device message
// fetched.
func (t *ToDeviceTable) Messages(userID, deviceID string, from, limit int64) (msgs []json.RawMessage, upTo int64, err error) {
	upTo = from
	var rows []ToDeviceRow
	err = t.db.Select(&rows,
		`SELECT position, message FROM syncv3_to_device_messages WHERE user_id = $1 AND device_id = $2 AND position > $3 ORDER BY position ASC LIMIT $4`,
		userID, deviceID, from, limit,
	)
	if len(rows) == 0 {
		return
	}
	msgs = make([]json.RawMessage, len(rows))
	for i := range rows {
		msgs[i] = json.RawMessage(rows[i].Message)
		m := gjson.ParseBytes(msgs[i])
		msgId := m.Get(`content.org\.matrix\.msgid`).Str
		if msgId != "" {
			logger.Info().Str("msgid", msgId).Str("user", userID).Str("device", deviceID).Msg("ToDeviceTable.Messages")
		}
	}
	upTo = rows[len(rows)-1].Position
	return
}

func (t *ToDeviceTable) InsertMessages(userID, deviceID string, msgs []json.RawMessage) (pos int64, err error) {
	var lastPos int64
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		var unackPos int64
		err = txn.QueryRow(`SELECT unack_pos FROM syncv3_to_device_ack_pos WHERE user_id=$1 AND device_id=$2`, userID, deviceID).Scan(&unackPos)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("unable to select unacked pos: %s", err)
		}

		// Some of these events may be "cancel" actions. If we find events for the unique key of this event, then delete them
		// and ignore the "cancel" action.
		cancels := []string{}
		allRequests := make(map[string]struct{})
		allCancels := make(map[string]struct{})

		rows := make([]ToDeviceRow, len(msgs))
		for i := range msgs {
			m := gjson.ParseBytes(msgs[i])
			rows[i] = ToDeviceRow{
				UserID:   userID,
				DeviceID: deviceID,
				Message:  string(msgs[i]),
				Type:     m.Get("type").Str,
				Sender:   m.Get("sender").Str,
			}
			msgId := m.Get(`content.org\.matrix\.msgid`).Str
			if msgId != "" {
				logger.Debug().Str("msgid", msgId).Str("user", userID).Str("device", deviceID).Msg("ToDeviceTable.InsertMessages")
			}
			switch rows[i].Type {
			case "m.room_key_request":
				action := m.Get("content.action").Str
				if action == "request" {
					rows[i].Action = ActionRequest
				} else if action == "request_cancellation" {
					rows[i].Action = ActionCancel
				}
				// "the same request_id and requesting_device_id fields, sent by the same user."
				key := fmt.Sprintf("%s-%s-%s-%s", rows[i].Type, rows[i].Sender, m.Get("content.requesting_device_id").Str, m.Get("content.request_id").Str)
				rows[i].UniqueKey = &key
			}
			if rows[i].Action == ActionCancel && rows[i].UniqueKey != nil {
				cancels = append(cancels, *rows[i].UniqueKey)
				allCancels[*rows[i].UniqueKey] = struct{}{}
			} else if rows[i].Action == ActionRequest && rows[i].UniqueKey != nil {
				allRequests[*rows[i].UniqueKey] = struct{}{}
			}
		}
		if len(cancels) > 0 {
			var cancelled []string
			// delete action: request events which have the same unique key, for this device inbox, only if they are not sent to the client already (unacked)
			err = txn.Select(&cancelled, `DELETE FROM syncv3_to_device_messages WHERE unique_key = ANY($1) AND user_id = $2 AND device_id = $3 AND position > $4 RETURNING unique_key`,
				pq.StringArray(cancels), userID, deviceID, unackPos)
			if err != nil {
				return fmt.Errorf("failed to delete cancelled events: %s", err)
			}
			cancelledInDBSet := make(map[string]struct{}, len(cancelled))
			for _, ukey := range cancelled {
				cancelledInDBSet[ukey] = struct{}{}
			}
			// do not insert the cancelled unique keys
			newRows := make([]ToDeviceRow, 0, len(rows))
			for i := range rows {
				if rows[i].UniqueKey != nil {
					ukey := *rows[i].UniqueKey
					_, exists := cancelledInDBSet[ukey]
					if exists {
						continue // the request was deleted so don't insert the cancel
					}
					// we may be requesting and cancelling in one go, check it and ignore if so
					_, reqExists := allRequests[ukey]
					_, cancelExists := allCancels[ukey]
					if reqExists && cancelExists {
						continue
					}
				}
				newRows = append(newRows, rows[i])
			}
			rows = newRows
		}
		// we may have nothing to do if the entire set of events were cancellations
		if len(rows) == 0 {
			return nil
		}

		chunks := sqlutil.Chunkify(7, MaxPostgresParameters, ToDeviceRowChunker(rows))
		for _, chunk := range chunks {
			result, err := txn.NamedQuery(`INSERT INTO syncv3_to_device_messages (user_id, device_id, message, event_type, sender, action, unique_key)
        VALUES (:user_id, :device_id, :message, :event_type, :sender, :action, :unique_key) RETURNING position`, chunk)
			if err != nil {
				return err
			}
			for result.Next() {
				if err = result.Scan(&lastPos); err != nil {
					result.Close()
					return err
				}
			}
			result.Close()
		}
		return nil
	})
	return lastPos, err
}
