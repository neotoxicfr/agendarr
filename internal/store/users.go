package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// User is an agendarr account, always backed by a Plex account.
type User struct {
	ID        int64
	PlexID    int64
	Username  string
	Email     string
	Thumb     string
	IsOwner   bool
	IsAdmin   bool
	PlexToken string // sealed
	Disabled  bool
	CreatedAt time.Time
	LastLogin time.Time
	CheckedAt time.Time
}

// ErrOwnerExists is returned when a second owner is claimed.
var ErrOwnerExists = errors.New("an owner already exists")

const userCols = `id, plex_id, username, email, thumb, is_owner, is_admin, plex_token, disabled, created_at, last_login, checked_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created, login, checked int64
	err := row.Scan(&u.ID, &u.PlexID, &u.Username, &u.Email, &u.Thumb, &u.IsOwner, &u.IsAdmin,
		&u.PlexToken, &u.Disabled, &created, &login, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.CreatedAt, u.LastLogin, u.CheckedAt = unix(created), unix(login), unix(checked)
	return u, err
}

func unix(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s, 0)
}

// HasOwner reports whether the first-run setup has been completed.
func (s *Store) HasOwner(ctx context.Context) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE is_owner = 1`).Scan(&n)
	return n > 0, err
}

// UserByID returns a user.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// UserByPlexID returns a user by Plex account id.
func (s *Store) UserByPlexID(ctx context.Context, plexID int64) (User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE plex_id = ?`, plexID))
}

// Users lists every user, owner first, then admins, then by name.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY is_owner DESC, is_admin DESC, username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Profile is what a sign-in refreshes on the user row.
type Profile struct {
	PlexID    int64
	Username  string
	Email     string
	Thumb     string
	PlexToken string // sealed
}

// SignIn creates or refreshes the user after a successful Plex sign-in and
// re-enables it. Admin rights are left untouched.
func (s *Store) SignIn(ctx context.Context, p Profile, now time.Time) (User, error) {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO users (plex_id, username, email, thumb, plex_token, created_at, last_login, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (plex_id) DO UPDATE SET
			username = excluded.username, email = excluded.email, thumb = excluded.thumb,
			plex_token = excluded.plex_token, disabled = 0,
			last_login = excluded.last_login, checked_at = excluded.checked_at`,
		p.PlexID, p.Username, p.Email, p.Thumb, p.PlexToken, now.Unix(), now.Unix(), now.Unix())
	if err != nil {
		return User{}, fmt.Errorf("sign in: %w", err)
	}
	return s.UserByPlexID(ctx, p.PlexID)
}

// ClaimOwner makes the user the owner and an admin, in the same transaction
// as the check that no owner exists yet. The unique partial index on is_owner
// backs this up if two setups race.
func (s *Store) ClaimOwner(ctx context.Context, userID int64, settings map[string]string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE is_owner = 1`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrOwnerExists
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET is_owner = 1, is_admin = 1 WHERE id = ?`, userID); err != nil {
		return fmt.Errorf("claim owner: %w", err)
	}
	for k, v := range settings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetAdmin grants or revokes admin rights. The owner always stays admin.
func (s *Store) SetAdmin(ctx context.Context, userID int64, admin bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET is_admin = ? WHERE id = ? AND is_owner = 0`, admin, userID)
	return err
}

// DeleteUser removes a user (never the owner) and everything tied to it.
func (s *Store) DeleteUser(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND is_owner = 0`, userID)
	return err
}

// MarkChecked records the outcome of a periodic Plex access check.
func (s *Store) MarkChecked(ctx context.Context, userID int64, hasAccess bool, now time.Time) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET checked_at = ?, disabled = ? WHERE id = ?`,
		now.Unix(), !hasAccess, userID)
	return err
}

// UsersToCheck returns enabled users whose last access check is older than before.
func (s *Store) UsersToCheck(ctx context.Context, before time.Time) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+userCols+` FROM users WHERE disabled = 0 AND checked_at < ?`, before.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
