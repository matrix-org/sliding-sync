package state

import (
	"github.com/jmoiron/sqlx"
)

const MembershipLogOffsetStart = -1

// MembershipLogTable stores a log of membership changes for rooms, along with the corresponding
// event nid of the membership event. This table contains membership deltas between historical
// room snapshots and the latest snapshot for a room, so it does not grow unbounded. E.g:
//
//  2 days ago                         now
//  Snapshot: 56 <-membership logs->  Snapshot: 57
//  alice: join                       alice:leave
//  bob: leave                        bob: join
//
//  Membership logs: bob:join, charlie:join, alice:leave, charlie:leave
//
// The logs do not strictly have to be between 2 snapshots e.g in cases where you are invited to
// a room the server doesn't know about, there are no snapshots.
//
// This table is used when calculating incremental sync results, to know which events to return.
// When a snapshot is deleted due to no outstanding references to it, the corresponding membership
// logs are purged.
type MembershipLogTable struct {
	db *sqlx.DB
}

func NewMembershipLogTable(db *sqlx.DB) *MembershipLogTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_membership_logs (
		event_nid BIGINT NOT NULL,
		target_user TEXT NOT NULL,
		room_id TEXT NOT NULL,
		UNIQUE(event_nid, target_user, room_id)
	);
	`)
	return &MembershipLogTable{db}
}

// AppendMembership adds a new membership entry to the log. Call this when new m.room.member events arrive.
func (t *MembershipLogTable) AppendMembership(txn *sqlx.Tx, eventNID int64, roomID string, target string) error {
	_, err := txn.Exec(`
		INSERT INTO syncv3_membership_logs(event_nid, target_user, room_id) VALUES($1, $2, $3)
		ON CONFLICT (event_nid, target_user, room_id) DO NOTHING`,
		eventNID, target, roomID,
	)
	return err
}

// MembershipsBetween returns all membership changes for the given user between the two event NIDs.
// Call this when processing /sync
func (t *MembershipLogTable) MembershipsBetween(txn *sqlx.Tx, fromNIDExcl, toNIDIncl int64, targetUser string) (eventNIDs []int64, err error) {
	err = txn.Select(
		&eventNIDs, `SELECT event_nid FROM syncv3_membership_logs WHERE event_nid > $1 AND event_nid <= $2 AND target_user = $3`,
		fromNIDExcl, toNIDIncl, targetUser,
	)
	return
}

func (t *MembershipLogTable) MembershipsBetweenForRoom(txn *sqlx.Tx, fromNIDExcl, toNIDIncl int64, limit int, targetRoom string) (eventNIDs []int64, err error) {
	err = txn.Select(
		&eventNIDs, `SELECT event_nid FROM syncv3_membership_logs WHERE event_nid > $1 AND event_nid <= $2 AND room_id = $3 ORDER BY event_nid ASC LIMIT $4`,
		fromNIDExcl, toNIDIncl, targetRoom, limit,
	)
	return
}

// DeleteLogs between the given event NIDs for the given room ID.
// from is inclusive to allow references to the last snapshot.
// to is exlcusive to allow references to the current snapshot.
// Call this when removing snapshots. Returns the number of logs removed. Snapshots are only removed
// when clients advance their snapshot position.
func (t *MembershipLogTable) DeleteLogs(txn *sqlx.Tx, fromNIDIncl, toNIDExcl int64, roomID string) (int64, error) {
	result, err := txn.Exec(
		`DELETE FROM syncv3_membership_logs WHERE room_id = $1 AND event_nid >= $2 AND event_nid < $3`,
		roomID, fromNIDIncl, toNIDExcl,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
