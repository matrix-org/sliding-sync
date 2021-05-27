package state

import (
	"github.com/jmoiron/sqlx"
)

// SnapshotRefCountsTable maintains a counter per room snapshot which represents
// how many clients have this snapshot as their 'latest' state. These snapshots
// are paginatable by clients.
//
// The current state snapshot for a room always has a ref count > 0 (the reference is held by the server, not any clients)
// to prevent the current state snapshot from being garbage collected.
type SnapshotRefCountsTable struct {
}

func NewSnapshotRefCountsTable(db *sqlx.DB) *SnapshotRefCountsTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_snapshot_ref_counts (
		snapshot_id BIGINT PRIMARY KEY NOT NULL,
		ref_count BIGINT NOT NULL
	);
	`)
	return &SnapshotRefCountsTable{}
}

// DeleteEmptyRefs removes snapshot ref counts which are 0 and returns the snapshot IDs of the affected rows.
func (s *SnapshotRefCountsTable) DeleteEmptyRefs(txn *sqlx.Tx) (emptyRefs []int, err error) {
	err = txn.Select(&emptyRefs, `SELECT snapshot_id FROM syncv3_snapshot_ref_counts WHERE ref_count = 0`)
	if err != nil {
		return
	}
	_, err = txn.Exec(`DELETE FROM syncv3_snapshot_ref_counts WHERE ref_count = 0`)
	return
}

// Select a row based on its snapshot ID.
func (s *SnapshotRefCountsTable) Decrement(txn *sqlx.Tx, snapshotID int) (count int, err error) {
	err = txn.QueryRow(`UPDATE syncv3_snapshot_ref_counts SET ref_count = syncv3_snapshot_ref_counts.ref_count - 1 WHERE snapshot_id=$1 RETURNING ref_count`, snapshotID).Scan(&count)
	return
}

// Insert the row. Modifies SnapshotID to be the inserted primary key.
func (s *SnapshotRefCountsTable) Increment(txn *sqlx.Tx, snapshotID int) (count int, err error) {
	err = txn.QueryRow(`INSERT INTO syncv3_snapshot_ref_counts(snapshot_id, ref_count) VALUES ($1, $2)
	ON CONFLICT (snapshot_id) DO UPDATE SET ref_count=syncv3_snapshot_ref_counts.ref_count+1 RETURNING ref_count`, snapshotID, 1).Scan(&count)
	return
}
