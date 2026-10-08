-- +goose Up
-- The feed token is looked up by its SHA-256 (the "token" column now holds
-- the hash) and kept sealed so its owner can copy the URL again. A leaked
-- database therefore exposes no feed URL.
DELETE FROM ics_tokens;
ALTER TABLE ics_tokens ADD COLUMN sealed TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE ics_tokens DROP COLUMN sealed;
