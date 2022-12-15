package state

import (
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sqlutil"
)

type receiptEDU struct {
	Type    string `json:"type"`
	Content map[string]struct {
		Read        map[string]receiptInfo `json:"m.read,omitempty"`
		ReadPrivate map[string]receiptInfo `json:"m.read.private,omitempty"`
	} `json:"content"`
}

type receiptInfo struct {
	TS       int64  `json:"ts"`
	ThreadID string `json:"thread_id,omitempty"`
}

type ReceiptTable struct {
	db *sqlx.DB
}

func NewReceiptTable(db *sqlx.DB) *ReceiptTable {
	// we make 2 tables here to reduce the compound key size to be just room/user/thread and not
	// room/user/thread/receipt_type. This should help performance somewhat when querying. Other than
	// that, the tables are identical.
	tableNames := []string{
		"syncv3_receipts", "syncv3_receipts_private",
	}
	schema := `
	CREATE TABLE IF NOT EXISTS %s (
		room_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		thread_id TEXT NOT NULL,
		event_id TEXT NOT NULL,
		ts BIGINT NOT NULL,
		UNIQUE(room_id, user_id, thread_id)
	);
	-- for querying by events in the timeline, need to search by event id
	CREATE INDEX IF NOT EXISTS %s_by_event_idx ON %s(room_id, event_id);
	-- for querying all receipts for a user in a room, need to search by user id
	CREATE INDEX IF NOT EXISTS %s_by_user_idx ON %s(room_id, user_id);
	`
	for _, tableName := range tableNames {
		db.MustExec(fmt.Sprintf(schema, tableName, tableName, tableName, tableName, tableName))
	}
	return &ReceiptTable{db}
}

// Insert new receipts based on a receipt EDU
// Returns newly inserted receipts, or nil if there are no new receipts.
// These newly inserted receipts can then be sent to the API processes for live updates.
func (t *ReceiptTable) Insert(roomID string, ephEvent json.RawMessage) (receipts []internal.Receipt, err error) {
	readReceipts, privateReceipts, err := unpackReceiptsFromEDU(roomID, ephEvent)
	if err != nil {
		return nil, err
	}
	if len(readReceipts) == 0 && len(privateReceipts) == 0 {
		return nil, nil
	}
	err = sqlutil.WithTransaction(t.db, func(txn *sqlx.Tx) error {
		readReceipts, err = t.bulkInsert("syncv3_receipts", txn, readReceipts)
		if err != nil {
			return err
		}
		privateReceipts, err = t.bulkInsert("syncv3_receipts_private", txn, privateReceipts)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to insert receipts: %s", err)
	}
	// no new receipts
	if len(readReceipts) == 0 && len(privateReceipts) == 0 {
		return nil, nil
	}
	// combine together new receipts
	return append(readReceipts, privateReceipts...), nil
}

// Select all non-private receipts for the event IDs given. Events must be in the room ID given.
// The parsed receipts are returned so callers can use information in the receipts in further queries
// e.g to pull out profile information for users read receipts. Call PackReceiptsIntoEDU when sending to clients.
func (t *ReceiptTable) SelectReceiptsForEvents(roomID string, eventIDs []string) (receipts []internal.Receipt, err error) {
	err = t.db.Select(&receipts, `SELECT room_id, event_id, user_id, ts, thread_id FROM syncv3_receipts
		WHERE room_id=$1 AND event_id = ANY($2)`, roomID, pq.StringArray(eventIDs))
	return
}

// Select all (including private) receipts for this user in this room.
func (t *ReceiptTable) SelectReceiptsForUser(roomID, userID string) (receipts []internal.Receipt, err error) {
	err = t.db.Select(&receipts, `SELECT room_id, event_id, user_id, ts, thread_id FROM syncv3_receipts
	WHERE room_id=$1 AND user_id = $2`, roomID, userID)
	if err != nil {
		return nil, err
	}
	var privReceipts []internal.Receipt
	err = t.db.Select(&privReceipts, `SELECT room_id, event_id, user_id, ts, thread_id FROM syncv3_receipts_private
	WHERE room_id=$1 AND user_id = $2`, roomID, userID)
	for i := range privReceipts {
		privReceipts[i].IsPrivate = true
	}
	receipts = append(receipts, privReceipts...)
	return
}

func (t *ReceiptTable) bulkInsert(tableName string, txn *sqlx.Tx, receipts []internal.Receipt) (newReceipts []internal.Receipt, err error) {
	if len(receipts) == 0 {
		return
	}
	chunks := sqlutil.Chunkify(5, MaxPostgresParameters, ReceiptChunker(receipts))
	var eventID string
	var roomID string
	var threadID string
	var userID string
	var ts int64
	for _, chunk := range chunks {
		rows, err := txn.NamedQuery(`
			INSERT INTO `+tableName+` AS old (room_id, event_id, user_id, ts, thread_id)
			VALUES (:room_id, :event_id, :user_id, :ts, :thread_id) ON CONFLICT (room_id, user_id, thread_id) DO UPDATE SET event_id=excluded.event_id, ts=excluded.ts WHERE old.event_id <> excluded.event_id
			RETURNING room_id, user_id, thread_id, event_id, ts`, chunk)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			if err := rows.Scan(&roomID, &userID, &threadID, &eventID, &ts); err != nil {
				rows.Close()
				return nil, err
			}
			newReceipts = append(newReceipts, internal.Receipt{
				RoomID:    roomID,
				EventID:   eventID,
				UserID:    userID,
				TS:        ts,
				ThreadID:  threadID,
				IsPrivate: tableName == "syncv3_receipts_private",
			})
		}
		rows.Close()
	}
	return
}

// PackReceiptsIntoEDU bundles all the receipts into a single m.receipt EDU, suitable for sending down
// client connections.
func PackReceiptsIntoEDU(receipts []internal.Receipt) (json.RawMessage, error) {
	newReceiptEDU := receiptEDU{
		Type: "m.receipt",
		Content: make(map[string]struct {
			Read        map[string]receiptInfo `json:"m.read,omitempty"`
			ReadPrivate map[string]receiptInfo `json:"m.read.private,omitempty"`
		}),
	}
	for _, r := range receipts {
		receiptsForEvent := newReceiptEDU.Content[r.EventID]
		if r.IsPrivate {
			if receiptsForEvent.ReadPrivate == nil {
				receiptsForEvent.ReadPrivate = make(map[string]receiptInfo)
			}
			receiptsForEvent.ReadPrivate[r.UserID] = receiptInfo{
				TS:       r.TS,
				ThreadID: r.ThreadID,
			}
		} else {
			if receiptsForEvent.Read == nil {
				receiptsForEvent.Read = make(map[string]receiptInfo)
			}
			receiptsForEvent.Read[r.UserID] = receiptInfo{
				TS:       r.TS,
				ThreadID: r.ThreadID,
			}
		}
		newReceiptEDU.Content[r.EventID] = receiptsForEvent
	}
	return json.Marshal(newReceiptEDU)
}

func unpackReceiptsFromEDU(roomID string, ephEvent json.RawMessage) (readReceipts, privateReceipts []internal.Receipt, err error) {
	// unpack the receipts, of the form:
	//  {
	//		"content": {
	//		  "$1435641916114394fHBLK:matrix.org": {
	//			"m.read": {
	//			  "@rikj:jki.re": {
	//				"ts": 1436451550453,
	//  			"thread_id": "$aaabbbccc"
	//			  }
	//			},
	//			"m.read.private": {
	//			  "@self:example.org": {
	//				"ts": 1661384801651
	//			  }
	//			}
	//		  }
	//		},
	//		"type": "m.receipt"
	//  }
	var edu receiptEDU
	if err := json.Unmarshal(ephEvent, &edu); err != nil {
		return nil, nil, err
	}
	if edu.Type != "m.receipt" {
		return
	}
	for eventID, content := range edu.Content {
		for userID, val := range content.Read {
			readReceipts = append(readReceipts, internal.Receipt{
				UserID:   userID,
				RoomID:   roomID,
				EventID:  eventID,
				TS:       val.TS,
				ThreadID: val.ThreadID,
			})
		}
		for userID, val := range content.ReadPrivate {
			privateReceipts = append(privateReceipts, internal.Receipt{
				UserID:    userID,
				RoomID:    roomID,
				EventID:   eventID,
				TS:        val.TS,
				ThreadID:  val.ThreadID,
				IsPrivate: true,
			})
		}
	}
	return readReceipts, privateReceipts, nil
}

type ReceiptChunker []internal.Receipt

func (c ReceiptChunker) Len() int {
	return len(c)
}
func (c ReceiptChunker) Subslice(i, j int) sqlutil.Chunker {
	return c[i:j]
}
