package state

import (
	"database/sql"

	"github.com/jmoiron/sqlx"
)

type SnapshotRefCountsTable struct {
	db *sqlx.DB
}

func NewSnapshotRefCountsTable(postgresURI string) *SnapshotRefCountsTable {
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		log.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_snapshot_ref_counts (
		snapshot_id BIGINT PRIMARY KEY NOT NULL,
		ref_count BIGINT NOT NULL
	);
	`)
	return &SnapshotRefCountsTable{
		db: db,
	}
}

// Select a row based on its snapshot ID.
func (s *SnapshotRefCountsTable) Decrement(tx *sql.Tx, snapshotID int) (count int, err error) {
	err = tx.QueryRow(`UPDATE syncv3_snapshot_ref_counts SET ref_count = syncv3_snapshot_ref_counts.ref_count - 1 WHERE snapshot_id=$1 RETURNING ref_count`, snapshotID).Scan(&count)
	return
}

// Insert the row. Modifies SnapshotID to be the inserted primary key.
func (s *SnapshotRefCountsTable) Increment(tx *sql.Tx, snapshotID int) (count int, err error) {
	err = tx.QueryRow(`INSERT INTO syncv3_snapshot_ref_counts(snapshot_id, ref_count) VALUES ($1, $2)
	ON CONFLICT (snapshot_id) DO UPDATE SET ref_count=syncv3_snapshot_ref_counts.ref_count+1 RETURNING ref_count`, snapshotID, 1).Scan(&count)
	return
}
