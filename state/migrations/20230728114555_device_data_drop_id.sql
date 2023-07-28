-- +goose Up
-- +goose StatementBegin
ALTER TABLE syncv3_device_data DROP COLUMN IF EXISTS id;
DROP SEQUENCE IF EXISTS syncv3_device_data_seq;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE SEQUENCE IF NOT EXISTS syncv3_device_data_seq;
ALTER TABLE syncv3_device_data ADD COLUMN IF NOT EXISTS id BIGINT PRIMARY KEY NOT NULL DEFAULT nextval('syncv3_device_data_seq') ;
-- +goose StatementEnd
