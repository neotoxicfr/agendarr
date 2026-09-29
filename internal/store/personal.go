package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// UserMedia returns the external ids of one personal source, and when it was
// last synced (zero if never).
func (s *Store) UserMedia(ctx context.Context, userID int64, source string) (map[string]bool, time.Time, string, error) {
	var at int64
	var syncErr string
	err := s.DB.QueryRowContext(ctx, `SELECT synced_at, error FROM user_media_sync WHERE user_id = ? AND source = ?`,
		userID, source).Scan(&at, &syncErr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, "", nil
	}
	if err != nil {
		return nil, time.Time{}, "", err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT media_key FROM user_media WHERE user_id = ? AND source = ?`, userID, source)
	if err != nil {
		return nil, time.Time{}, "", err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, time.Time{}, "", err
		}
		set[k] = true
	}
	return set, time.Unix(at, 0), syncErr, rows.Err()
}

// SetUserMedia replaces one personal source. On a sync error the previous
// set is kept and only the error is recorded.
func (s *Store) SetUserMedia(ctx context.Context, userID int64, source string, keys []string, syncErr error, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	msg := ""
	if syncErr != nil {
		msg = syncErr.Error()
	} else {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_media WHERE user_id = ? AND source = ?`, userID, source); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO user_media (user_id, source, media_key) VALUES (?, ?, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, k := range keys {
			if _, err := stmt.ExecContext(ctx, userID, source, k); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_media_sync (user_id, source, synced_at, error) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, source) DO UPDATE SET synced_at = excluded.synced_at, error = excluded.error`,
		userID, source, now.Unix(), msg); err != nil {
		return err
	}
	return tx.Commit()
}

// PlexGuids returns the cached external ids of ratingKeys; missing keys are
// absent from the map.
func (s *Store) PlexGuids(ctx context.Context, keys []string) (map[string][]string, error) {
	out := make(map[string][]string, len(keys))
	const batch = 400
	for i := 0; i < len(keys); i += batch {
		j := min(i+batch, len(keys))
		args := make([]any, j-i)
		for n, k := range keys[i:j] {
			args[n] = k
		}
		rows, err := s.DB.QueryContext(ctx, `SELECT rating_key, keys FROM plex_guids WHERE rating_key IN (?`+
			strings.Repeat(",?", j-i-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var rk, ks string
			if err := rows.Scan(&rk, &ks); err != nil {
				rows.Close()
				return nil, err
			}
			out[rk] = strings.Fields(ks)
		}
		rows.Close()
	}
	return out, nil
}

// SavePlexGuids caches resolved ratingKeys (an empty slice marks a deleted item).
func (s *Store) SavePlexGuids(ctx context.Context, m map[string][]string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for rk, ks := range m {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plex_guids (rating_key, keys, fetched_at) VALUES (?, ?, ?)
			ON CONFLICT (rating_key) DO UPDATE SET keys = excluded.keys, fetched_at = excluded.fetched_at`,
			rk, strings.Join(ks, " "), now.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Prefs returns the user's saved preferences (JSON), or "{}".
func (s *Store) Prefs(ctx context.Context, userID int64) (string, error) {
	var p string
	err := s.DB.QueryRowContext(ctx, `SELECT prefs FROM user_prefs WHERE user_id = ?`, userID).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "{}", nil
	}
	return p, err
}

// SetPrefs saves the user's preferences.
func (s *Store) SetPrefs(ctx context.Context, userID int64, prefs string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user_prefs (user_id, prefs) VALUES (?, ?)
		ON CONFLICT (user_id) DO UPDATE SET prefs = excluded.prefs`, userID, prefs)
	return err
}
