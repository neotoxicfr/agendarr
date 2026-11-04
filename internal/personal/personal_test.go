package personal

import (
	"context"
	"fmt"
	"log/slog"
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
	u, err := st.SignIn(ctx, store.Profile{PlexID: 7, Username: "u"}, time.Now())
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
	u, err := st.SignIn(ctx, store.Profile{PlexID: 7, Username: "u"}, time.Now())
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
