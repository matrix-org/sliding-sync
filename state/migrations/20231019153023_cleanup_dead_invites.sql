-- +goose Up
-- +goose StatementBegin
WITH
    dead_invites(user_id, room_id) AS (
        SELECT syncv3_invites.user_id, syncv3_invites.room_id
        FROM syncv3_invites
            JOIN syncv3_rooms USING (room_id)
            JOIN syncv3_snapshots ON (syncv3_snapshots.snapshot_id = syncv3_rooms.current_snapshot_id)
            JOIN syncv3_events ON (
                    event_nid = ANY (membership_events)
                AND state_key = syncv3_invites.user_id
                AND NOT (membership = 'invite' OR membership = '_invite')
            )
    )
DELETE FROM syncv3_invites
WHERE (user_id, room_id) IN (SELECT * FROM dead_invites);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- no-op
-- +goose StatementEnd
