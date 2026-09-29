-- +goose Up
-- ratingKey -> external ids, so a user's history is resolved once, not on
-- every refresh. Items deleted from Plex keep an empty row.
CREATE TABLE plex_guids (
    rating_key TEXT PRIMARY KEY,
    keys       TEXT NOT NULL, -- space separated: "tvdb:1 tmdb:2 imdb:tt3"
    fetched_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE plex_guids;
