-- +goose Up
-- The account behind a user is a Plex account (its numeric id, kept as an
-- integer by the column's affinity) or a Jellyfin account (its id as a dashed
-- GUID, never read as a number). Renamed rather than rebuilt: dropping users
-- would cascade to every table that references it.
ALTER TABLE users RENAME COLUMN plex_id TO account_id;
ALTER TABLE users RENAME COLUMN plex_token TO token;

-- +goose Down
ALTER TABLE users RENAME COLUMN token TO plex_token;
ALTER TABLE users RENAME COLUMN account_id TO plex_id;
