package personal

import (
	"context"
	"errors"
	"fmt"
	"github.com/neotoxicfr/agendarr/internal/jellyfin"
	"github.com/neotoxicfr/agendarr/internal/plex"
	"github.com/neotoxicfr/agendarr/internal/seerr"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
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

// On a Jellyfin install the sources come from the server with its API key,
// for the user's Jellyfin id: favorites, recently played series, watched.
func TestJellyfinSources(t *testing.T) {
	ctx := context.Background()
	const friend = "0534d2b624ff479cb4e2d05fded41061"
	file := func(w http.ResponseWriter, name string) {
		b, err := os.ReadFile("../jellyfin/testdata/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case !strings.Contains(r.Header.Get("Authorization"), `Token="key"`):
			w.WriteHeader(http.StatusUnauthorized)
		case q.Get("ids") != "":
			file(w, "series-by-id")
		case q.Get("userId") != friend:
			io.WriteString(w, `{"Items":[]}`)
		case q.Get("filters") == "IsFavorite":
			file(w, "favorites")
		case q.Get("includeItemTypes") == "Episode":
			file(w, "played-episodes")
		default:
			file(w, "played-movies")
		}
	}))
	defer ts.Close()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(Deps{Store: st, Log: slog.New(slog.DiscardHandler),
		Jellyfin: func(context.Context) (*jellyfin.Client, error) {
			return &jellyfin.Client{BaseURL: ts.URL, Token: "key"}, nil
		}})
	// Played on 2026-11-04 (the bench); "watching" looks 90 days back from then.
	s.now = func() time.Time { return time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC) }
	u, _ := st.SignIn(ctx, store.Profile{AccountID: jellyfin.FormatID(friend), Username: "friend"}, time.Now())
	for src, want := range map[string]string{SourceWatchlist: "tmdb:4247", SourceWatching: "tvdb:393201", SourceWatched: "tmdb:4247"} {
		if r := s.For(ctx, u, []string{src}); !r.Keys[want] || len(r.Errors) > 0 {
			t.Errorf("%s: %+v", src, r)
		}
	}
}

func newTestService(t *testing.T, d Deps) (*Service, *store.Store, store.User) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.SignIn(context.Background(), store.Profile{AccountID: "7", Username: "u"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.Store, d.Log = st, slog.New(slog.DiscardHandler)
	return New(d), st, u
}

// A failed sync with a usable previous set is answered at once with that set
// and the error's own code, however old the error (PERF-a3-01, UX-a3-02).
func TestFailedSyncServesPreviousSet(t *testing.T) {
	ctx := context.Background()
	block := make(chan struct{})
	defer close(block)
	s, st, u := newTestService(t, Deps{Jellyfin: func(context.Context) (*jellyfin.Client, error) {
		<-block // the media server hangs
		return nil, errors.New("down")
	}})
	for _, age := range []time.Duration{10 * time.Second, 2 * time.Minute} {
		if err := st.SetUserMedia(ctx, u.ID, SourceWatchlist, []string{"tmdb:1"}, nil, time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := st.SetUserMedia(ctx, u.ID, SourceWatchlist, nil, ErrPlexSignedOut, time.Now().Add(-age)); err != nil {
			t.Fatal(err)
		}
		s.syncedMedia(fmt.Sprintf("%d/%s", u.ID, SourceWatchlist), nil)
		done := make(chan Result, 1)
		go func() { done <- s.For(ctx, u, []string{SourceWatchlist}) }()
		select {
		case r := <-done:
			if !r.Keys["tmdb:1"] || r.Errors[SourceWatchlist] != "plex_signed_out" || r.Missing {
				t.Fatalf("age %v: %+v", age, r)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("age %v: the request waited for the retry", age)
		}
	}
}

// A transient failure with no key at all is Missing (the iCal feed answers
// 503); a permanent one is not (EDGE-a3-04).
func TestResultMissing(t *testing.T) {
	ctx := context.Background()
	var err error
	s, _, u := newTestService(t, Deps{Seerrs: func(context.Context) ([]*seerr.Client, error) { return nil, err }})
	err = errors.New("seerr down")
	if r := s.For(ctx, u, []string{SourceSeerr}); !r.Missing {
		t.Fatalf("transient: %+v", r)
	}
	s.seerrErr = nil
	err = ErrNoSeerr
	if r := s.For(ctx, u, []string{SourceSeerr}); r.Missing || r.Errors[SourceSeerr] != "no_seerr" {
		t.Fatalf("permanent: %+v", r)
	}
}

// Once Seerr is gone, its old requests stop being served (EDGE-a3-03).
func TestSeerrIndexDropped(t *testing.T) {
	ctx := context.Background()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user":
			io.WriteString(w, `{"pageInfo":{"results":1},"results":[{"id":3,"displayName":"u"}]}`)
		case "/api/v1/request":
			io.WriteString(w, `{"pageInfo":{"results":1},"results":[{"id":1,"type":"movie","requestedBy":{"id":3},"media":{"tmdbId":100}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	var clients []*seerr.Client
	s, _, u := newTestService(t, Deps{Seerrs: func(context.Context) ([]*seerr.Client, error) { return clients, nil }})
	clients = []*seerr.Client{seerr.New(ts.URL, "k")}
	if _, err := s.seerr(ctx); err != nil {
		t.Fatal(err)
	}
	if s.RequestIndex(ctx) == nil {
		t.Fatal("no index")
	}
	clients = nil
	now := time.Now().Add(time.Hour)
	s.now = func() time.Time { return now }
	s.For(ctx, u, []string{SourceSeerr}) // stale: served, refreshed behind
	deadline := time.Now().Add(3 * time.Second)
	for s.RequestIndex(ctx) != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.RequestIndex(ctx) != nil {
		t.Fatal("index still served")
	}
	if r := s.For(ctx, u, []string{SourceSeerr}); r.Errors[SourceSeerr] != "no_seerr" {
		t.Fatalf("%+v", r)
	}
}

// resolve drops TVDB keys of movies and refuses a truncated answer (EDGE-a1-01, EDGE-a3-06).
func TestResolve(t *testing.T) {
	ctx := context.Background()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys := strings.Split(strings.TrimPrefix(r.URL.Path, "/library/metadata/"), ",")
		if slices.Contains(keys, "bad") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var md []string
		for _, k := range keys {
			md = append(md, fmt.Sprintf(`{"ratingKey":%q,"Guid":[{"id":"tmdb://9"},{"id":"tvdb://5555"}]}`, k))
		}
		io.WriteString(w, `{"MediaContainer":{"Metadata":[`+strings.Join(md, ",")+`]}}`)
	}))
	defer ts.Close()
	s, st, _ := newTestService(t, Deps{})
	srv := &plex.Server{BaseURL: ts.URL, Token: "t"}
	out, err := s.resolve(ctx, srv, []string{"film"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(out, "tmdb:9") || slices.Contains(out, "tvdb:5555") {
		t.Fatalf("movie keys: %v", out)
	}
	out, err = s.resolve(ctx, srv, []string{"show"}, map[string]bool{"show": true})
	if err != nil || !slices.Contains(out, "tmdbtv:9") || !slices.Contains(out, "tvdb:5555") {
		t.Fatalf("show keys: %v %v", out, err)
	}
	// 100 good keys, then a batch that fails: the good ones stay cached, the call fails.
	rks := make([]string, 0, 101)
	for i := range 100 {
		rks = append(rks, fmt.Sprintf("g%d", i))
	}
	rks = append(rks, "bad")
	if _, err := s.resolve(ctx, srv, rks, nil); err == nil {
		t.Fatal("truncated answer taken for a sync")
	}
	if c, _ := st.PlexGuids(ctx, rks, time.Now()); len(c) != 100 {
		t.Fatalf("cached %d, want 100", len(c))
	}
}
