-- +goose Up
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE users (
    id          INTEGER PRIMARY KEY,
    plex_id     INTEGER NOT NULL UNIQUE,
    username    TEXT    NOT NULL,
    email       TEXT    NOT NULL DEFAULT '',
    thumb       TEXT    NOT NULL DEFAULT '',
    is_owner    INTEGER NOT NULL DEFAULT 0,
    is_admin    INTEGER NOT NULL DEFAULT 0,
    plex_token  TEXT    NOT NULL DEFAULT '', -- sealed with internal/secret
    disabled    INTEGER NOT NULL DEFAULT 0,  -- lost access to the Plex server
    created_at  INTEGER NOT NULL,
    last_login  INTEGER NOT NULL DEFAULT 0,
    checked_at  INTEGER NOT NULL DEFAULT 0   -- last server-access check
);
-- At most one owner, enforced by the database rather than by a race-prone check.
CREATE UNIQUE INDEX users_single_owner ON users (is_owner) WHERE is_owner = 1;

-- alexedwards/scs sqlite3store layout.
CREATE TABLE sessions (
    token  TEXT PRIMARY KEY,
    data   BLOB NOT NULL,
    expiry REAL NOT NULL
);
CREATE INDEX sessions_expiry_idx ON sessions (expiry);

CREATE TABLE instances (
    id        INTEGER PRIMARY KEY,
    kind      TEXT    NOT NULL CHECK (kind IN ('radarr', 'sonarr', 'seerr')),
    name      TEXT    NOT NULL,
    url       TEXT    NOT NULL,
    api_key   TEXT    NOT NULL, -- sealed
    enabled   INTEGER NOT NULL DEFAULT 1,
    position  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE user_prefs (
    user_id INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    prefs   TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE ics_tokens (
    token      TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL
);
CREATE INDEX ics_tokens_user_idx ON ics_tokens (user_id);

-- Per-user sets of external ids (Seerr requests, Plex watchlist, history...),
-- refreshed in the background. source = 'seerr' | 'watching' | 'watchlist' | 'watched'.
CREATE TABLE user_media (
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    source     TEXT    NOT NULL,
    media_key  TEXT    NOT NULL, -- 'tmdb:123', 'tvdb:456', 'imdb:tt..'
    PRIMARY KEY (user_id, source, media_key)
);
CREATE TABLE user_media_sync (
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    source     TEXT    NOT NULL,
    synced_at  INTEGER NOT NULL,
    error      TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, source)
);

-- +goose Down
DROP TABLE user_media_sync;
DROP TABLE user_media;
DROP TABLE ics_tokens;
DROP TABLE user_prefs;
DROP TABLE instances;
DROP TABLE sessions;
DROP TABLE users;
DROP TABLE settings;
