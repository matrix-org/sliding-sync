package state

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type SnapshotRow struct {
	SnapshotID int64         `db:"snapshot_id"`
	RoomID     string        `db:"room_id"`
	Events     pq.Int64Array `db:"events"`
}

// SnapshotTable stores room state snapshots. Each snapshot has a unique numeric ID.
// Not every event will be associated with a snapshot.
type SnapshotTable struct {
	db *sqlx.DB
}

func NewSnapshotsTable(db *sqlx.DB) *SnapshotTable {
	// make sure tables are made
	db.MustExec(`
	CREATE SEQUENCE IF NOT EXISTS syncv3_snapshots_seq;
	CREATE TABLE IF NOT EXISTS syncv3_snapshots (
		snapshot_id BIGINT PRIMARY KEY DEFAULT nextval('syncv3_snapshots_seq'),
		room_id TEXT NOT NULL,
		events BIGINT[] NOT NULL,
		UNIQUE(snapshot_id, room_id)
	);
	`)
	return &SnapshotTable{db}
}

func (t *SnapshotTable) CurrentSnapshots() (map[string][]int64, error) {
	rows, err := t.db.Query(
		`SELECT syncv3_rooms.room_id, events FROM syncv3_snapshots JOIN syncv3_rooms ON syncv3_snapshots.snapshot_id = syncv3_rooms.current_snapshot_id`,
	)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]int64)
	defer rows.Close()
	for rows.Next() {
		var eventNIDs pq.Int64Array
		var roomID string
		if err = rows.Scan(&roomID, &eventNIDs); err != nil {
			return nil, err
		}
		result[roomID] = eventNIDs
	}
	return result, nil
}

// Select a row based on its snapshot ID.
func (s *SnapshotTable) Select(txn *sqlx.Tx, snapshotID int64) (row SnapshotRow, err error) {
	if snapshotID == 0 {
		err = fmt.Errorf("SnapshotTable.Select: snapshot ID requested is 0")
		return
	}
	err = txn.Get(&row, `SELECT * FROM syncv3_snapshots WHERE snapshot_id = $1`, snapshotID)
	return
}

// Insert the row. Modifies SnapshotID to be the inserted primary key.
func (s *SnapshotTable) Insert(txn *sqlx.Tx, row *SnapshotRow) error {
	var id int64
	err := txn.QueryRow(`INSERT INTO syncv3_snapshots(room_id, events) VALUES($1, $2) RETURNING snapshot_id`, row.RoomID, row.Events).Scan(&id)
	row.SnapshotID = id
	return err
}

// Delete the snapshot IDs given
func (s *SnapshotTable) Delete(txn *sqlx.Tx, snapshotIDs []int64) error {
	query, args, err := sqlx.In(`DELETE FROM syncv3_snapshots WHERE snapshot_id = ANY(?)`, pq.Int64Array(snapshotIDs))
	if err != nil {
		return err
	}
	query = txn.Rebind(query)
	_, err = txn.Exec(query, args...)
	return err
}
