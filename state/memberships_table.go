package state

import (
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type MembershipsTable struct {
	db *sqlx.DB
}

func NewMembershipsTable(db *sqlx.DB) *MembershipsTable {
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_memberships (
		event_nid BIGINT NOT NULL,
		state_key TEXT NOT NULL,
		snapshot_ids BIGINT[] NOT NULL
	);
	CREATE INDEX IF NOT EXISTS syncv3_memberships_state_key_idx ON syncv3_memberships (state_key);
	CREATE UNIQUE INDEX IF NOT EXISTS syncv3_memberships_event_nid_idx ON syncv3_memberships (event_nid, state_key);
`)
	return &MembershipsTable{db}
}

var totalTimeRefreshing time.Duration
var countRefresh int

func (m *MembershipsTable) Insert(txn *sqlx.Tx, snapshot SnapshotRow) error {
	start := time.Now()

	_, err := txn.Exec(`
	INSERT INTO syncv3_memberships (event_nid, state_key, snapshot_ids)
	SELECT event_nid, state_key, $1 FROM syncv3_events WHERE event_nid = ANY ($2)
	ON CONFLICT (event_nid, state_key) DO UPDATE SET snapshot_ids = (select array( select distinct unnest(array_append(syncv3_memberships.snapshot_ids, $3)) ) )
	`, pq.Array([]int64{snapshot.SnapshotID}), pq.Array(snapshot.MembershipEvents), snapshot.SnapshotID)
	if err != nil {
		return err
	}

	if len(snapshot.MembershipEvents) > 1 {
		_, err = txn.Exec(`
	UPDATE syncv3_memberships
	SET snapshot_ids = (select array( select distinct unnest(array_append(syncv3_memberships.snapshot_ids, $1)) ) )
	WHERE event_nid = ANY($2)`, snapshot.SnapshotID, snapshot.MembershipEvents)
	}

	dur := time.Since(start)
	totalTimeRefreshing += dur
	countRefresh++

	logger.Trace().Msgf("inserted memberships event in %s (total: %s (%d times))", dur, totalTimeRefreshing, countRefresh)

	return err
}

// selectEventNIDs queries the eventNIDs for a user in particular snapshots
// Unexported, since it is only needed in tests
func (m *MembershipsTable) selectEventNIDs(txn *sqlx.Tx, userIDs []string, snapshots []int64) (eventNIDs []int64, err error) {

	qry, args, err := sqlx.In(`
	SELECT syncv3_memberships.event_nid
    FROM syncv3_memberships
    WHERE state_key = ANY (?) AND snapshot_ids @> (?::bigint[])`,
		pq.Array(userIDs), pq.Array(snapshots),
	)
	if err != nil {
		return nil, err
	}
	qry = txn.Rebind(qry)

	logger.Trace().Msgf("%s\n%#v - %#v - %#v", qry, args, userIDs, snapshots)

	err = txn.Select(&eventNIDs, qry, args...)

	return eventNIDs, err
}
