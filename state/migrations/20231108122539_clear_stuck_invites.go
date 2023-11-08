package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upClearStuckInvites, downClearStuckInvites)
}

// The purpose of this migration is to find users who have rooms which have
// not been properly processed by the proxy and invalidate their since token
// so they will do an initial sync on the next poller startup. This is specifically
// targeting stuck invites, where there is an invite in the invites table but
// the room is already joined. This is usually (always?) due to missing a create
// event when the room was joined, caused by a synapse bug outlined in
// https://github.com/matrix-org/sliding-sync/issues/367
// This isn't exclusively a problem with invites, though it manifests more clearly there.
func upClearStuckInvites(ctx context.Context, tx *sql.Tx) error {
	// The syncv3_unread table is updated any time A) a room is in rooms.join and B) the unread count has changed,
	// where nil != 0. Therefore, we can use this table as a proxy for "have we seen a v2 response which has put this
	// room into rooms.join"? For every room in rooms.join, we should have seen a create event for it, and hence have
	// an entry in syncv3_rooms. If we do not have an entry in syncv3_rooms but do have an entry in syncv3_unread, this
	// implies we failed to properly store this joined room and therefore the user who the unread marker is for should be
	// reset to force an initial sync. On matrix.org, of the users using sliding sync, this will catch around ~1.82% of users
	rows, err := tx.QueryContext(ctx, `
		SELECT distinct(user_id) FROM syncv3_unread
		WHERE room_id NOT IN (
			SELECT room_id
			FROM syncv3_rooms
		) GROUP BY user_id
	`)
	defer rows.Close()
	if err != nil {
		return fmt.Errorf("failed to select bad users: %w", err)
	}

	var usersToInvalidate []string
	for rows.Next() {
		var userID string
		err = rows.Scan(&userID)
		if err != nil {
			return fmt.Errorf("failed to scan user: %w", err)
		}
		usersToInvalidate = append(usersToInvalidate, userID)
	}
	logger.Info().Int("len_invalidate_users", len(usersToInvalidate)).Msg("invalidating users")
	if len(usersToInvalidate) < 50 {
		logger.Info().Strs("invalidate_users", usersToInvalidate).Msg("invalidating users")
	}

	// for each user:
	// - reset their since token for all devices
	// - remove any outstanding invites (we'll be told about them again when they initial sync)
	res, err := tx.ExecContext(ctx, `
	UPDATE syncv3_sync2_devices SET since='' WHERE user_id=ANY($1)
	`, pq.StringArray(usersToInvalidate))
	if err != nil {
		return fmt.Errorf("failed to invalidate since tokens: %w", err)
	}
	ra, _ := res.RowsAffected()
	logger.Info().Int64("num_devices", ra).Msg("reset since tokens")

	res, err = tx.ExecContext(ctx, `
	DELETE FROM syncv3_invites WHERE user_id=ANY($1)
	`, pq.StringArray(usersToInvalidate))
	if err != nil {
		return fmt.Errorf("failed to remove outstanding invites: %w", err)
	}
	ra, _ = res.RowsAffected()
	logger.Info().Int64("num_invites", ra).Msg("reset invites")
	return nil
}

func downClearStuckInvites(ctx context.Context, tx *sql.Tx) error {
	// we can't roll this back
	return nil
}
