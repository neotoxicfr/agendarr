-- +goose Up
-- Releases a user follows by hand from a detail sheet (a movie or a series).
CREATE TABLE follows (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    grp        TEXT NOT NULL, -- "movie:tmdb:1", "series:tvdb:2"
    title      TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, grp)
);

-- A user's notifications: a release became available, or its file was upgraded.
CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    kind       TEXT NOT NULL, -- available | upgraded
    grp        TEXT NOT NULL,
    title      TEXT NOT NULL,
    detail     TEXT NOT NULL, -- JSON, rendered in the user's language
    read       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notifications_user ON notifications (user_id, id DESC);

-- Browsers subscribed to Web Push, with the language of the person.
CREATE TABLE push_subs (
    endpoint   TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    lang       TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

-- Last file seen for each release, to notice when one appears or changes.
CREATE TABLE media_state (
    key        TEXT PRIMARY KEY, -- movie group, or episode id
    has_file   INTEGER NOT NULL,
    sig        TEXT NOT NULL,
    seen_at    INTEGER NOT NULL
);

-- +goose Down
DROP TABLE media_state;
DROP TABLE push_subs;
DROP TABLE notifications;
DROP TABLE follows;
