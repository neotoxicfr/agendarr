package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// HashToken is how feed tokens are looked up.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// SetICSToken replaces the user's feed token.
func (s *Store) SetICSToken(ctx context.Context, userID int64, token, sealed string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM ics_tokens WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ics_tokens (token, user_id, created_at, sealed) VALUES (?, ?, ?, ?)`,
		HashToken(token), userID, now.Unix(), sealed); err != nil {
		return err
	}
	return tx.Commit()
}

// ICSToken returns the user's sealed feed token, or "" when none.
func (s *Store) ICSToken(ctx context.Context, userID int64) (sealed string, created time.Time, err error) {
	var at int64
	err = s.DB.QueryRowContext(ctx, `SELECT sealed, created_at FROM ics_tokens WHERE user_id = ?`, userID).Scan(&sealed, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	return sealed, time.Unix(at, 0), err
}

// UserByICSToken resolves a feed token to its enabled user.
func (s *Store) UserByICSToken(ctx context.Context, token string) (User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userColsPrefixed("u")+` FROM ics_tokens t
		JOIN users u ON u.id = t.user_id WHERE t.token = ? AND u.disabled = 0`, HashToken(token)))
}

// DeleteICSToken revokes the user's feed.
func (s *Store) DeleteICSToken(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM ics_tokens WHERE user_id = ?`, userID)
	return err
}

func userColsPrefixed(p string) string {
	out := ""
	for i, c := range []string{"id", "plex_id", "username", "email", "thumb", "is_owner", "is_admin", "plex_token", "disabled", "created_at", "last_login", "checked_at"} {
		if i > 0 {
			out += ", "
		}
		out += p + "." + c
	}
	return out
}
