package state

import (
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type SnapshotRow struct {
	SnapshotID int           `db:"snapshot_id"`
	RoomID     string        `db:"room_id"`
	Events     pq.Int64Array `db:"events"`
}

type SnapshotTable struct {
	db *sqlx.DB
}

func NewSnapshotsTable(postgresURI string) *SnapshotTable {
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		log.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}
	// make sure tables are made
	db.MustExec(`
	CREATE SEQUENCE IF NOT EXISTS syncv3_snapshots_seq;
	CREATE TABLE IF NOT EXISTS syncv3_snapshots (
		snapshot_id BIGINT PRIMARY KEY DEFAULT nextval('syncv3_snapshots_seq'),
		room_id TEXT NOT NULL,
		events BIGINT[] NOT NULL
	);
	`)
	return &SnapshotTable{
		db: db,
	}
}

// Select a row based on its snapshot ID.
func (s *SnapshotTable) Select(snapshotID int) (row SnapshotRow, err error) {
	err = s.db.Get(&row, `SELECT * FROM syncv3_snapshots WHERE snapshot_id = $1`, snapshotID)
	return
}

// Insert the row. Modifies SnapshotID to be the inserted primary key.
func (s *SnapshotTable) Insert(row *SnapshotRow) error {
	var id int
	err := s.db.QueryRow(`INSERT INTO syncv3_snapshots(room_id, events) VALUES($1, $2) RETURNING snapshot_id`, row.RoomID, row.Events).Scan(&id)
	row.SnapshotID = id
	return err
}
