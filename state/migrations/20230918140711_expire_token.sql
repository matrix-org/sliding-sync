-- +goose Up
-- +goose StatementBegin
ALTER TABLE syncv3_sync2_tokens ADD COLUMN IF NOT EXISTS expired BOOL DEFAULT FALSE NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE syncv3_sync2_tokens DROP COLUMN IF EXISTS expired;
-- +goose StatementEnd
