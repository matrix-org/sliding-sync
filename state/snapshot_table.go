package state

import (
	"fmt"
	"github.com/matrix-org/sliding-sync/sqlutil"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type SnapshotRow struct {
	SnapshotID       int64         `db:"snapshot_id"`
	RoomID           string        `db:"room_id"`
	OtherEvents      pq.Int64Array `db:"events"`
	MembershipEvents pq.Int64Array `db:"membership_events"`
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
		membership_events BIGINT[] NOT NULL,
		UNIQUE(snapshot_id, room_id)
	);
	`)
	return &SnapshotTable{db}
}

func (t *SnapshotTable) CurrentSnapshots(txn *sqlx.Tx) (map[string][]int64, error) {
	rows, err := txn.Query(
		`SELECT syncv3_rooms.room_id, events, membership_events FROM syncv3_snapshots JOIN syncv3_rooms ON syncv3_snapshots.snapshot_id = syncv3_rooms.current_snapshot_id`,
	)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]int64)
	defer rows.Close()
	for rows.Next() {
		var eventNIDs pq.Int64Array
		var memberEventNIDs pq.Int64Array
		var roomID string
		if err = rows.Scan(&roomID, &eventNIDs, &memberEventNIDs); err != nil {
			return nil, err
		}
		result[roomID] = append(eventNIDs, memberEventNIDs...)
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
	if row.MembershipEvents == nil {
		row.MembershipEvents = []int64{}
	}
	if row.OtherEvents == nil {
		row.OtherEvents = []int64{}
	}
	err := txn.QueryRow(
		`INSERT INTO syncv3_snapshots(room_id, events, membership_events) VALUES($1, $2, $3) RETURNING snapshot_id`,
		row.RoomID, row.OtherEvents, row.MembershipEvents,
	).Scan(&id)
	row.SnapshotID = id
	return err
}

// BulkInsert multiple rows. The caller MUST provide SnapshotIDs for these rows that
// have been reserved from the DB using ReserveSnapshotIDs.
func (s *SnapshotTable) BulkInsert(txn *sqlx.Tx, rows []SnapshotRow) error {
	chunks := sqlutil.Chunkify2[SnapshotRow](4, MaxPostgresParameters, rows)
	for _, chunk := range chunks {
		_, err := txn.NamedExec(`
		INSERT INTO syncv3_snapshots (snapshot_id, room_id, events, membership_events)
        VALUES (:snapshot_id, :room_id, :events, :membership_events)`, chunk)
		if err != nil {
			return err
		}
	}
	return nil
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

// ReserveSnapshotIDs asks the database to reserve N snapshot IDs that will never be
// used by the database elsewhere. The application is then free to create and insert
// snapshots with these IDs. The returned IDs are guaranteed to be increasing (and
// therefore distinct).
func (s *SnapshotTable) ReserveSnapshotIDs(txn *sqlx.Tx, N int64) ([]int64, error) {
	ids := make([]int64, 0, N)
	// We use the same trick that Synapse does, see e.g.
	// https://github.com/matrix-org/synapse/blob/92014fbf7286afa9099d162b22feda868af820f8/synapse/storage/util/sequence.py#L114
	// For paranoia's sake I've put in an explicit ORDER BY clause here.
	err := txn.Select(&ids, `
		SELECT nextval('syncv3_snapshots_seq') AS ids
		FROM generate_series(1, $1)
		ORDER BY ids ASC;
		`, N,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to reserve snapshot IDs: %w", err)
	}
	return ids, nil
}
