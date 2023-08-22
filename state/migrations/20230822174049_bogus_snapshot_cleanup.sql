-- +goose Up
-- This migration runs a one-time cleanup job to delete rooms with dodgy state snapshots.
-- A room is dodgy if its current snapshot lacks a create event.
CREATE TEMPORARY TABLE bogus_rooms AS (
    SELECT room_id, current_snapshot_id
    FROM syncv3_rooms
    WHERE room_id NOT IN (
        SELECT room_id
        FROM syncv3_events
        WHERE event_type = 'm.room.create'
          AND state_key = ''
    )
);

DELETE
FROM syncv3_events
WHERE room_id IN (SELECT room_id FROM bogus_rooms);

DELETE
FROM syncv3_snapshots
WHERE room_id IN (SELECT room_id FROM bogus_rooms);

DELETE
FROM syncv3_rooms
WHERE room_id IN (SELECT room_id FROM bogus_rooms);
-- +goose Down
-- downgrading is a no-op.
