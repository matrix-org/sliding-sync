-- +goose Up
ALTER TABLE IF EXISTS syncv3_events
    ADD COLUMN IF NOT EXISTS missing_previous BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN syncv3_events.missing_previous IS
    'True iff the previous timeline event is not known to the proxy.';

-- +goose Down
ALTER TABLE IF EXISTS syncv3_events
    DROP COLUMN IF EXISTS missing_previous;
