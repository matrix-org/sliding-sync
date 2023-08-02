-- +goose Up
-- +goose StatementBegin
ALTER TABLE IF EXISTS syncv3_device_data ADD COLUMN IF NOT EXISTS dataj JSONB;
UPDATE syncv3_device_data SET dataj = encode(data, 'escape')::JSONB;
ALTER TABLE IF EXISTS syncv3_device_data DROP COLUMN IF EXISTS data;
ALTER TABLE IF EXISTS syncv3_device_data RENAME COLUMN dataj TO data;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE IF EXISTS syncv3_device_data ADD COLUMN IF NOT EXISTS datab BYTEA;
UPDATE syncv3_device_data SET datab = (data::TEXT)::BYTEA;
ALTER TABLE IF EXISTS syncv3_device_data DROP COLUMN IF EXISTS data;
ALTER TABLE IF EXISTS syncv3_device_data RENAME COLUMN datab TO data;
-- +goose StatementEnd
