package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
	a, err := s.SignIn(ctx, Profile{PlexID: 1, Username: "neo", PlexToken: "x"}, now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.SignIn(ctx, Profile{PlexID: 2, Username: "friend"}, now)
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
	a, err = s.SignIn(ctx, Profile{PlexID: 1, Username: "neo2"}, now)
	if err != nil || !a.IsAdmin || a.Username != "neo2" {
		t.Fatalf("owner after demote/delete/sign-in: %+v %v", a, err)
	}
	// A disabled user is re-enabled by signing in again.
	_ = s.MarkChecked(ctx, b.ID, false, now)
	if b, _ = s.UserByID(ctx, b.ID); !b.Disabled {
		t.Fatal("not disabled")
	}
	if b, _ = s.SignIn(ctx, Profile{PlexID: 2, Username: "friend"}, now); b.Disabled {
		t.Fatal("still disabled")
	}
	users, _ := s.Users(ctx)
	if len(users) != 2 || users[0].ID != a.ID {
		t.Fatalf("users order %+v", users)
	}
}
