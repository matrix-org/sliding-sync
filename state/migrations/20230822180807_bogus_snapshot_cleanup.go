package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog/log"
)

func init() {
	goose.AddMigrationContext(upBogusSnapshotCleanup, downBogusSnapshotCleanup)
}

func upBogusSnapshotCleanup(ctx context.Context, tx *sql.Tx) error {
	// Run a one-off script to delete rooms with bogus snapshots.
	// "Bogus" means "lacking a create event".
	bogusRooms, err := getBogusRooms(ctx, tx)
	if err != nil {
		return err
	}
	if len(bogusRooms) == 0 {
		return nil
	}
	log.Info().Strs("room_ids", bogusRooms).
		Msgf("Found %d bogus rooms to cleanup", len(bogusRooms))

	tables := []string{"syncv3_snapshots", "syncv3_events", "syncv3_rooms"}
	for _, table := range tables {
		err = deleteFromTable(ctx, tx, table, bogusRooms)
		if err != nil {
			return err
		}
	}
	return nil
}

func deleteFromTable(ctx context.Context, tx *sql.Tx, table string, roomIDs []string) error {
	result, err := tx.ExecContext(
		ctx,
		`DELETE FROM `+table+` WHERE room_id = ANY($1)`,
		pq.StringArray(roomIDs))
	if err != nil {
		return fmt.Errorf("failed to delete from %s: %w", table, err)
	}
	ra, err := result.RowsAffected()
	if err != nil {
		log.Warn().Err(err).Msgf("Couldn't get number of rows deleted from %s", table)
	} else {
		log.Info().Msgf("Deleted %d rows from %s", ra, table)
	}
	return nil
}

func getBogusRooms(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT room_id
		FROM syncv3_rooms
		WHERE room_id NOT IN (
			SELECT room_id
			FROM syncv3_events
			WHERE event_type = 'm.room.create' AND state_key = ''
		)
	`)
	defer rows.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to select bogus rooms: %w", err)
	}

	var bogusRooms []string
	for rows.Next() {
		var roomID string
		err = rows.Scan(&roomID)
		if err != nil {
			return nil, fmt.Errorf("failed to scan bogus room: %w", err)
		}
		bogusRooms = append(bogusRooms, roomID)
	}

	return bogusRooms, nil
}

func downBogusSnapshotCleanup(ctx context.Context, tx *sql.Tx) error {
	// No-op.
	return nil
}
