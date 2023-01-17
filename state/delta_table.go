package state

import (
	"database/sql"

	"github.com/jmoiron/sqlx"
)

type DeltaRow struct {
	ID       int64  `db:"id"`
	DeviceID string `db:"device_id"`
	Data     []byte `db:"data"`
}

type DeltaTableInterface interface {
	CreateDeltaState(deviceID string) (row *DeltaRow, err error)
	Load(id int64, deviceID string) (row *DeltaRow, err error)
	Update(row *DeltaRow) error
}

// DeltaTable stores client state for a device, to reduce the data we send back to clients.
type DeltaTable struct {
	db *sqlx.DB
}

func NewDeltaTable(db *sqlx.DB) *DeltaTable {
	// make sure tables are made
	db.MustExec(`
	CREATE SEQUENCE IF NOT EXISTS syncv3_delta_seq;
	CREATE TABLE IF NOT EXISTS syncv3_delta (
		id BIGINT PRIMARY KEY NOT NULL DEFAULT nextval('syncv3_delta_seq'),
		device_id TEXT NOT NULL, -- scope to prevent clients using each others delta states
		data BYTEA NOT NULL -- cbor
	);
	`)
	return &DeltaTable{
		db: db,
	}
}

// CreateDeltaState creates a new delta state for this device. Returns the row to form the delta state.
func (t *DeltaTable) CreateDeltaState(deviceID string) (row *DeltaRow, err error) {
	var id int64
	err = t.db.QueryRow(`INSERT INTO syncv3_delta(device_id, data) VALUES($1, $2) RETURNING id`, deviceID, []byte{}).Scan(&id)
	row = &DeltaRow{
		ID:       id,
		DeviceID: deviceID,
		Data:     []byte{},
	}
	return
}

func (t *DeltaTable) Update(row *DeltaRow) error {
	_, err := t.db.Exec(
		`UPDATE syncv3_delta SET data=$1 WHERE id=$2 AND device_id=$3`, row.Data, row.ID, row.DeviceID,
	)
	return err
}

func (t *DeltaTable) Load(id int64, deviceID string) (row *DeltaRow, err error) {
	var data []byte
	err = t.db.QueryRow(
		`SELECT data FROM syncv3_delta WHERE id=$1 AND device_id=$2`, id, deviceID,
	).Scan(&data)
	// sql.ErrNoRows may mean that this client doesn't have a delta state with this id
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err == nil {
		row = &DeltaRow{
			ID:       id,
			DeviceID: deviceID,
			Data:     data,
		}
	}
	return
}
