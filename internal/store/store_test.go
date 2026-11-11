package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/pressly/goose/v3"
	"testing"
	"time"
)

// Cached ratingKeys expire: a month for matches, a day for empty ones, so a
// later match in Plex is picked up (EDGE-a3-07).
func TestPlexGuidsExpire(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)
	now := time.Now()
	save := func(rk string, ks []string, age time.Duration) {
		if err := s.SavePlexGuids(ctx, map[string][]string{rk: ks}, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	save("fresh", []string{"tmdb:1"}, time.Hour)
	save("emptyfresh", nil, time.Hour)
	save("empty2d", nil, 48*time.Hour)
	save("old40d", []string{"tmdb:1"}, 40*24*time.Hour)
	got, err := s.PlexGuids(ctx, []string{"fresh", "emptyfresh", "empty2d", "old40d"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["empty2d"]; ok {
		t.Error("empty row older than a day still served")
	}
	if _, ok := got["old40d"]; ok {
		t.Error("row older than 30 days still served")
	}
	if _, ok := got["fresh"]; !ok {
		t.Error("fresh row dropped")
	}
	if _, ok := got["emptyfresh"]; !ok {
		t.Error("fresh empty row dropped")
	}
}

func newTest(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSettings(t *testing.T) {
	s, ctx := newTest(t), context.Background()
	if v, err := s.Setting(ctx, "missing"); err != nil || v != "" {
		t.Fatalf("missing: %q, %v", v, err)
	}
	for _, v := range []string{"a", "b"} {
		if err := s.SetSetting(ctx, "k", v); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Setting(ctx, "k"); got != v {
			t.Fatalf("got %q, want %q", got, v)
		}
	}
}

func TestOwnerClaim(t *testing.T) {
	s, ctx := newTest(t), context.Background()
	now := time.Unix(1_700_000_000, 0)
	a, err := s.SignIn(ctx, Profile{AccountID: "1", Username: "neo", Token: "x"}, now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.SignIn(ctx, Profile{AccountID: "2", Username: "friend"}, now)
	if ok, _ := s.HasOwner(ctx); ok {
		t.Fatal("owner before claim")
	}
	if err := s.ClaimOwner(ctx, a.ID, map[string]string{"plex_server_id": "srv1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimOwner(ctx, b.ID, nil); !errors.Is(err, ErrOwnerExists) {
		t.Fatalf("second claim: %v", err)
	}
	if v, _ := s.Setting(ctx, "plex_server_id"); v != "srv1" {
		t.Fatalf("setting %q", v)
	}
	a, _ = s.UserByID(ctx, a.ID)
	if !a.IsOwner || !a.IsAdmin {
		t.Fatalf("owner flags %+v", a)
	}
	// The owner cannot be demoted or deleted; a sign-in keeps admin rights.
	_ = s.SetAdmin(ctx, a.ID, false)
	_ = s.DeleteUser(ctx, a.ID)
	a, err = s.SignIn(ctx, Profile{AccountID: "1", Username: "neo2"}, now)
	if err != nil || !a.IsAdmin || a.Username != "neo2" {
		t.Fatalf("owner after demote/delete/sign-in: %+v %v", a, err)
	}
	// A disabled user is re-enabled by signing in again.
	_ = s.MarkChecked(ctx, b.ID, false, now)
	if b, _ = s.UserByID(ctx, b.ID); !b.Disabled {
		t.Fatal("not disabled")
	}
	if b, _ = s.SignIn(ctx, Profile{AccountID: "2", Username: "friend"}, now); b.Disabled {
		t.Fatal("still disabled")
	}
	users, _ := s.Users(ctx)
	if len(users) != 2 || users[0].ID != a.ID {
		t.Fatalf("users order %+v", users)
	}
}

func TestNotifications(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)
	u, err := s.SignIn(ctx, Profile{AccountID: "7", Username: "a"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	if err := s.SetFollow(ctx, u.ID, "series:tvdb:1", "Below", true, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < keepNotifications+3; i++ {
		g := "movie:tmdb:2"
		if i == keepNotifications+2 {
			g = "series:tvdb:1"
		}
		if _, err := s.AddNotification(ctx, u.ID, Notification{At: now, Kind: "available", Group: g, Title: "x", Detail: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	list, unread, err := s.Notifications(ctx, u.ID, 500)
	if err != nil || len(list) != keepNotifications || unread != keepNotifications {
		t.Fatalf("trim: %d %d %v", len(list), unread, err)
	}
	if !list[0].Follows || list[1].Follows {
		t.Fatalf("follows flag: %+v %+v", list[0], list[1])
	}
	if err := s.MarkRead(ctx, u.ID, []int64{list[0].ID, list[1].ID}); err != nil {
		t.Fatal(err)
	}
	if _, unread, _ = s.Notifications(ctx, u.ID, 1); unread != keepNotifications-2 {
		t.Fatalf("unread %d", unread)
	}
	_ = s.SetFollow(ctx, u.ID, "series:tvdb:1", "", false, now)
	if f, _ := s.Followers(ctx); len(f) != 0 {
		t.Fatalf("followers %v", f)
	}
	if err := s.SaveMediaStates(ctx, map[string]MediaState{"k": {true, "4K"}}, now); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.MediaStates(ctx); !m["k"].HasFile || m["k"].Sig != "4K" {
		t.Fatalf("state %v", m)
	}
}

// A v5 database keeps its users and what hangs off them when users.plex_id
// becomes account_id; Plex ids read back as decimal strings.
func TestAccountMigration(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, "migrations", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, plex_id, username, plex_token, created_at) VALUES (1, 42, 'neo', 'sealed', 1);
		INSERT INTO user_prefs (user_id, prefs) VALUES (1, '{"view":"week"}');
		INSERT INTO follows (user_id, grp, title, created_at) VALUES (1, 'movie:tmdb:1', 'Dune', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: db}
	u, err := s.UserByAccount(ctx, "42")
	if err != nil || u.ID != 1 || u.AccountID != "42" || u.Token != "sealed" {
		t.Fatalf("user: %+v %v", u, err)
	}
	var prefs string
	var follows int
	_ = db.QueryRowContext(ctx, `SELECT prefs FROM user_prefs WHERE user_id = 1`).Scan(&prefs)
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM follows WHERE user_id = 1`).Scan(&follows)
	if prefs != `{"view":"week"}` || follows != 1 {
		t.Fatalf("lost: prefs %q, follows %d", prefs, follows)
	}
	// A Jellyfin id (dashed GUID) sits next to it and is not read as a number.
	const jf = "8f2c1a4e-5b6d-4e7f-9a0b-1c2d3e4f5a6b"
	j, err := s.SignIn(ctx, Profile{AccountID: jf, Username: "jelly"}, time.Now())
	if err != nil || j.AccountID != jf {
		t.Fatalf("jellyfin: %+v %v", j, err)
	}
	if again, err := s.UserByAccount(ctx, jf); err != nil || again.ID != j.ID {
		t.Fatalf("lookup: %+v %v", again, err)
	}
}
