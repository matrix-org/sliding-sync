package state

import (
	"github.com/jmoiron/sqlx"
)

// SnapshotRefCountsTable maintains a list of entities who are aware of certain snapshots, aka
// clients who have this snapshot as their 'latest' state. These snapshots
// are paginatable by clients. Each entity can only have 1 snapshot for each room.
//
// Entities are any string value to decouple this implementation from sync v3 semantics. In practice,
// entities are confirmed/unconfirmed sync sessions.
type SnapshotRefCountsTable struct {
}

func NewSnapshotRefCountsTable(db *sqlx.DB) *SnapshotRefCountsTable {
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_snapshot_ref_counts (
		entity TEXT NOT NULL,
		snapshot_id BIGINT NOT NULL,
		room_id TEXT NOT NULL,
		UNIQUE(entity, room_id)
	);
	`)
	return &SnapshotRefCountsTable{}
}

// MoveSnapshotRefForEntity migrates the snapshot for the current entity's room ID. If there is no existing
// snapshot for this room, entity tuple then one is created
func (s *SnapshotRefCountsTable) MoveSnapshotRefForEntity(txn *sqlx.Tx, entity, roomID string, snapshotID int) error {
	_, err := txn.Exec(
		`INSERT INTO syncv3_snapshot_ref_counts(entity, room_id, snapshot_id) VALUES($1, $2, $3)
		ON CONFLICT (entity, room_id) DO UPDATE SET snapshot_id = $3`,
		entity, roomID, snapshotID,
	)
	return err
}

// NumRefs counts the number of references for this snapshot ID.
func (s *SnapshotRefCountsTable) NumRefs(txn *sqlx.Tx, snapshotID int) (count int, err error) {
	err = txn.QueryRow(
		`SELECT count(*) FROM syncv3_snapshot_ref_counts WHERE snapshot_id=$1`, snapshotID,
	).Scan(&count)
	return
}
