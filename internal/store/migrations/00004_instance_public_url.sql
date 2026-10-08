-- +goose Up
-- The address a browser can open (links for admins), when the instance is
-- reached through an internal one such as http://radarr:7878.
ALTER TABLE instances ADD COLUMN public_url TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE instances DROP COLUMN public_url;
