package store

import (
	"context"
	"strings"
	"time"
)

// Follow is a release a user follows by hand.
type Follow struct {
	Group string
	Title string
}

// SetFollow follows (on) or unfollows a movie or series.
func (s *Store) SetFollow(ctx context.Context, userID int64, group, title string, on bool, now time.Time) error {
	if !on {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM follows WHERE user_id = ? AND grp = ?`, userID, group)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO follows (user_id, grp, title, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, grp) DO UPDATE SET title = excluded.title`, userID, group, title, now.Unix())
	return err
}

// Follows returns the groups a user follows.
func (s *Store) Follows(ctx context.Context, userID int64) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT grp FROM follows WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out[g] = true
	}
	return out, rows.Err()
}

// Followers returns, for each followed group, who follows it.
func (s *Store) Followers(ctx context.Context) (map[string][]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT grp, user_id FROM follows`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var g string
		var id int64
		if err := rows.Scan(&g, &id); err != nil {
			return nil, err
		}
		out[g] = append(out[g], id)
	}
	return out, rows.Err()
}

// Notification is one entry of a user's history.
type Notification struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // available | upgraded
	Group   string    `json:"group"`
	Title   string    `json:"title"`
	Detail  string    `json:"detail"` // JSON, rendered by the browser
	Read    bool      `json:"read"`
	Follows bool      `json:"follows"` // the user follows the group by hand
}

// keepNotifications is how many entries a user keeps; older ones go.
const keepNotifications = 200

// AddNotification records one and trims the user's history.
func (s *Store) AddNotification(ctx context.Context, userID int64, n Notification) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO notifications (user_id, created_at, kind, grp, title, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, n.At.Unix(), n.Kind, n.Group, n.Title, n.Detail)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	_, err = s.DB.ExecContext(ctx, `DELETE FROM notifications WHERE user_id = ? AND id NOT IN
		(SELECT id FROM notifications WHERE user_id = ? ORDER BY id DESC LIMIT ?)`, userID, userID, keepNotifications)
	return id, err
}

// Notifications returns a user's history, newest first, and the unread count.
func (s *Store) Notifications(ctx context.Context, userID int64, limit int) ([]Notification, int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT n.id, n.created_at, n.kind, n.grp, n.title, n.detail, n.read, f.grp IS NOT NULL
		FROM notifications n LEFT JOIN follows f ON f.user_id = n.user_id AND f.grp = n.grp
		WHERE n.user_id = ? ORDER BY n.id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var at int64
		if err := rows.Scan(&n.ID, &at, &n.Kind, &n.Group, &n.Title, &n.Detail, &n.Read, &n.Follows); err != nil {
			return nil, 0, err
		}
		n.At = time.Unix(at, 0)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int
	err = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM notifications WHERE user_id = ? AND read = 0`, userID).Scan(&unread)
	return out, unread, err
}

// MarkRead acknowledges some notifications of a user, or all when ids is empty.
func (s *Store) MarkRead(ctx context.Context, userID int64, ids []int64) error {
	if len(ids) == 0 {
		_, err := s.DB.ExecContext(ctx, `UPDATE notifications SET read = 1 WHERE user_id = ?`, userID)
		return err
	}
	args := []any{userID}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE notifications SET read = 1 WHERE user_id = ? AND id IN (?`+
		strings.Repeat(",?", len(ids)-1)+`)`, args...)
	return err
}

// PushSub is a browser subscribed to Web Push.
type PushSub struct {
	UserID   int64
	Endpoint string
	P256dh   string
	Auth     string
	Lang     string
}

// SavePushSub stores (or moves to this user) a browser subscription.
func (s *Store) SavePushSub(ctx context.Context, p PushSub, now time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO push_subs (endpoint, user_id, p256dh, auth, lang, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET user_id = excluded.user_id, p256dh = excluded.p256dh, auth = excluded.auth, lang = excluded.lang`,
		p.Endpoint, p.UserID, p.P256dh, p.Auth, p.Lang, now.Unix())
	return err
}

// DeletePushSub forgets a subscription; userID 0 deletes it whoever owns it.
func (s *Store) DeletePushSub(ctx context.Context, userID int64, endpoint string) error {
	if userID == 0 {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM push_subs WHERE endpoint = ?`, endpoint)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM push_subs WHERE endpoint = ? AND user_id = ?`, endpoint, userID)
	return err
}

// PushSubs returns a user's subscriptions.
func (s *Store) PushSubs(ctx context.Context, userID int64) ([]PushSub, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT user_id, endpoint, p256dh, auth, lang FROM push_subs WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSub
	for rows.Next() {
		var p PushSub
		if err := rows.Scan(&p.UserID, &p.Endpoint, &p.P256dh, &p.Auth, &p.Lang); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MediaState is the last file seen for a release.
type MediaState struct {
	HasFile bool
	Sig     string
}

// MediaStates returns every known state.
func (s *Store) MediaStates(ctx context.Context) (map[string]MediaState, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT key, has_file, sig FROM media_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MediaState{}
	for rows.Next() {
		var k string
		var m MediaState
		if err := rows.Scan(&k, &m.HasFile, &m.Sig); err != nil {
			return nil, err
		}
		out[k] = m
	}
	return out, rows.Err()
}

// SaveMediaStates records states and drops those unseen for 90 days.
func (s *Store) SaveMediaStates(ctx context.Context, m map[string]MediaState, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO media_state (key, has_file, sig, seen_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET has_file = excluded.has_file, sig = excluded.sig, seen_at = excluded.seen_at`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for k, v := range m {
		if _, err := stmt.ExecContext(ctx, k, v.HasFile, v.Sig, now.Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_state WHERE seen_at < ?`, now.Add(-90*24*time.Hour).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
