package personal

import (
	"context"
	"fmt"
	"github.com/neotoxicfr/agendarr/internal/seerr"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neotoxicfr/agendarr/internal/store"
)

// BenchmarkForWatched reads a fresh, synced "watched" source of a few
// thousand keys, as every calendar request with that filter does.
func BenchmarkForWatched(b *testing.B) {
	ctx := context.Background()
	st, err := store.OpenMemory()
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	u, err := st.SignIn(ctx, store.Profile{AccountID: "7", Username: "u"}, time.Now())
	if err != nil {
		b.Fatal(err)
	}
	keys := make([]string, 5000)
	for i := range keys {
		keys[i] = fmt.Sprintf("tmdb:%d", i)
	}
	if err := st.SetUserMedia(ctx, u.ID, SourceWatched, keys, nil, time.Now()); err != nil {
		b.Fatal(err)
	}
	s := New(Deps{Store: st, Log: slog.New(slog.DiscardHandler)})
	b.ReportAllocs()
	for b.Loop() {
		if r := s.For(ctx, u, []string{SourceWatched}); len(r.Keys) != len(keys) {
			b.Fatal(len(r.Keys), r.Errors)
		}
	}
}

// The store is read once, then served from memory; a user recreated with the
// same id (new CreatedAt) does not get the old user's set.
func TestMediaMirror(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, err := st.SignIn(ctx, store.Profile{AccountID: "7", Username: "u"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserMedia(ctx, u.ID, SourceWatched, []string{"tmdb:1"}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := New(Deps{Store: st, Log: slog.New(slog.DiscardHandler)})
	done := make(chan bool)
	for range 8 {
		go func() { done <- s.For(ctx, u, []string{SourceWatched, SourceWatched}).Keys["tmdb:1"] }()
	}
	for range 8 {
		if !<-done {
			t.Fatal("concurrent read")
		}
	}
	if err := st.SetUserMedia(ctx, u.ID, SourceWatched, []string{"tmdb:2"}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if r := s.For(ctx, u, []string{SourceWatched}); !r.Keys["tmdb:1"] {
		t.Fatalf("not served from memory: %v", r.Keys)
	}
	u.CreatedAt = u.CreatedAt.Add(time.Second)
	if r := s.For(ctx, u, []string{SourceWatched}); !r.Keys["tmdb:2"] {
		t.Fatalf("recreated user: %v", r.Keys)
	}
}

// A Jellyfin account finds its Seerr requests by jellyfinUserId, whichever
// form (dashes, case) each side writes the id in; an account Seerr does not
// know gets ErrNoSeerrUser.
func TestSeerrJellyfinAccount(t *testing.T) {
	ctx := context.Background()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user":
			io.WriteString(w, `{"pageInfo":{"results":1},"results":[{"id":3,"jellyfinUserId":"8F2C1A4E5B6D4E7F9A0B1C2D3E4F5A6B","displayName":"jelly"}]}`)
		case "/api/v1/request":
			io.WriteString(w, `{"pageInfo":{"results":1},"results":[{"id":1,"type":"movie","requestedBy":{"id":3},"media":{"tmdbId":100}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(Deps{Store: st, Log: slog.New(slog.DiscardHandler),
		Seerrs: func(context.Context) ([]*seerr.Client, error) { return []*seerr.Client{seerr.New(ts.URL, "k")}, nil }})
	known, _ := st.SignIn(ctx, store.Profile{AccountID: "8f2c1a4e-5b6d-4e7f-9a0b-1c2d3e4f5a6b", Username: "jelly"}, time.Now())
	if r := s.For(ctx, known, []string{SourceSeerr}); !r.Keys["tmdb:100"] || len(r.Errors) > 0 {
		t.Fatalf("known: %+v", r)
	}
	other, _ := st.SignIn(ctx, store.Profile{AccountID: "11111111-2222-3333-4444-555555555555", Username: "other"}, time.Now())
	if r := s.For(ctx, other, []string{SourceSeerr}); r.Errors[SourceSeerr] != Code(ErrNoSeerrUser) {
		t.Fatalf("unknown: %+v", r)
	}
}
