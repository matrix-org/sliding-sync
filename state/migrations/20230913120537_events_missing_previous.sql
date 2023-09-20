-- +goose Up
ALTER TABLE IF EXISTS syncv3_events
    ADD COLUMN IF NOT EXISTS missing_previous BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE IF EXISTS syncv3_events
    DROP COLUMN IF EXISTS missing_previous;
