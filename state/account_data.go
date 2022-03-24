package state

import (
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/matrix-org/sync-v3/sqlutil"
)

const AccountDataGlobalRoom = ""

type AccountData struct {
	UserID string `db:"user_id"`
	RoomID string `db:"room_id"`
	Type   string `db:"type"`
	Data   []byte `db:"data"`
}

// AccountDataTable stores the account data for users.
type AccountDataTable struct{}

func NewAccountDataTable(db *sqlx.DB) *AccountDataTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_account_data (
		user_id TEXT NOT NULL,
		room_id TEXT NOT NULL, -- optional if global
		type TEXT NOT NULL,
		data BYTEA NOT NULL,
		UNIQUE(user_id, room_id, type)
	);
	`)
	return &AccountDataTable{}
}

// Insert account data.
func (t *AccountDataTable) Insert(txn *sqlx.Tx, accDatas []AccountData) ([]AccountData, error) {
	// fold duplicates into one as we A: don't care about historical data, B: cannot use EXCLUDED if the
	// accDatas list has the same unique key twice in the same transaction.
	keys := map[string]*AccountData{}
	dedupedAccountData := make([]AccountData, 0, len(accDatas))
	for i := range accDatas {
		key := fmt.Sprintf("%s %s %s", accDatas[i].UserID, accDatas[i].RoomID, accDatas[i].Type)
		// later data always wins as it is more recent
		keys[key] = &accDatas[i]
	}
	// now make a new account data list with keys de-duped. This will resort the events but that's
	// fine as this is an atomic operation anyway.
	for _, ad := range keys {
		dedupedAccountData = append(dedupedAccountData, *ad)
	}
	chunks := sqlutil.Chunkify(4, MaxPostgresParameters, AccountDataChunker(dedupedAccountData))
	for _, chunk := range chunks {
		_, err := txn.NamedExec(`
		INSERT INTO syncv3_account_data (user_id, room_id, type, data)
        VALUES (:user_id, :room_id, :type, :data) ON CONFLICT (user_id, room_id, type) DO UPDATE SET data = EXCLUDED.data`, chunk)
		if err != nil {
			return nil, err
		}
	}
	return dedupedAccountData, nil
}

func (t *AccountDataTable) Select(txn *sqlx.Tx, userID, eventType, roomID string) (*AccountData, error) {
	var acc AccountData
	err := txn.Get(&acc, `SELECT user_id, room_id, type, data FROM syncv3_account_data
	WHERE user_id=$1 AND type=$2 AND room_id=$3`, userID, eventType, roomID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &acc, err
}

func (t *AccountDataTable) SelectMany(txn *sqlx.Tx, userID string, roomIDs ...string) (datas []AccountData, err error) {
	if len(roomIDs) == 0 {
		err = txn.Select(&datas, `SELECT user_id, room_id, type, data FROM syncv3_account_data
		WHERE user_id=$1 AND room_id = $2`, userID, AccountDataGlobalRoom)
		return
	}
	err = txn.Select(&datas, `SELECT user_id, room_id, type, data FROM syncv3_account_data
	WHERE user_id=$1 AND room_id=ANY($2)`, userID, pq.StringArray(roomIDs))
	return
}

type AccountDataChunker []AccountData

func (c AccountDataChunker) Len() int {
	return len(c)
}
func (c AccountDataChunker) Subslice(i, j int) sqlutil.Chunker {
	return c[i:j]
}
