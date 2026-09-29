package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neotoxicfr/agendarr/internal/arr"
)

func day(s string) *time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return &t
}

func TestFromMovieAndMerge(t *testing.T) {
	m := arr.Movie{ID: 1, Title: "Dune", TmdbID: 42, ImdbID: "tt1", InCinemas: day("2026-10-01"),
		DigitalRelease: day("2026-11-15"), Monitored: true,
		Images: []arr.Image{{CoverType: "poster", URL: "/MediaCover/1/poster.jpg?lastWrite=9"}}}
	hd := fromMovie(1, "Radarr", m)
	if len(hd) != 2 || hd[0].Release != ReleaseCinema || hd[1].Date != "2026-11-15" || hd[0].Poster != "/img/1/1" {
		t.Fatalf("fromMovie: %+v", hd)
	}
	m.HasFile = true
	uhd := fromMovie(2, "Radarr 4K", m)
	ep, ok := fromEpisode(3, "Sonarr", arr.Episode{SeasonNumber: 1, EpisodeNumber: 1, AirDateUTC: func() *time.Time {
		t := time.Date(2026, 10, 1, 1, 30, 0, 0, time.UTC)
		return &t
	}(), Series: arr.Series{Title: "Andor", TvdbID: 7}})
	if !ok || !ep.Premiere || ep.Group != "series:tvdb:7" {
		t.Fatalf("fromEpisode: %+v", ep)
	}
	got := merge(append(append(hd, uhd...), ep))
	if len(got) != 3 {
		t.Fatalf("merge: %d events", len(got))
	}
	// Same day: the timed episode sorts before the all-day movie.
	if got[0].Kind != "episode" || got[1].Release != ReleaseCinema {
		t.Fatalf("order: %s %s", got[0].ID, got[1].ID)
	}
	if len(got[1].Instances) != 2 || !got[1].HasFile {
		t.Fatalf("merged instances: %+v", got[1])
	}
	if _, ok := fromEpisode(3, "S", arr.Episode{}); ok {
		t.Fatal("undated episode kept")
	}
}

func TestServiceCache(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	var fail, block atomic.Bool
	s := New(func(context.Context) ([]Source, error) {
		return []Source{{ID: 1, Kind: "radarr", Name: "Radarr"}}, nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = func() time.Time { return now }
	release := make(chan struct{}, 10)
	s.fetch = func(_ context.Context, src Source, from, to time.Time) ([]Event, error) {
		calls.Add(1)
		if block.Load() {
			<-release
		}
		if fail.Load() {
			return nil, errors.New("down")
		}
		d := from.AddDate(0, 0, 14).Format(time.DateOnly)
		return []Event{{ID: d, Date: d, Title: "x", Instances: []string{src.Name}}}, nil
	}
	ctx := context.Background()
	from, to := *day("2026-10-01"), *day("2026-10-31")

	r, err := s.Range(ctx, from, to)
	// One day of margin on each side spans September and November too.
	if err != nil || len(r.Events) != 1 || calls.Load() != 3 || !r.Configured {
		t.Fatalf("first: %+v %v calls=%d", r, err, calls.Load())
	}
	s.Range(ctx, from, to)
	if calls.Load() != 3 {
		t.Fatalf("cached: calls=%d", calls.Load())
	}
	// Stale and the instance is down: the old events are still served at
	// once, then the refresh flags the instance.
	now = now.Add(TTL + time.Second)
	fail.Store(true)
	block.Store(true)
	r, _ = s.Range(ctx, from, to)
	if len(r.Events) != 1 {
		t.Fatalf("stale served: %+v", r)
	}
	block.Store(false)
	for i := 0; i < 3; i++ {
		release <- struct{}{}
	}
	waitFor(t, func() bool { return calls.Load() == 6 })
	waitFor(t, func() bool {
		r, _ = s.Range(ctx, from, to)
		return len(r.Failing) == 1
	})
	if len(r.Events) != 1 {
		t.Fatalf("events lost on failure: %+v", r)
	}
	s.Invalidate()
	fail.Store(false)
	r, _ = s.Range(ctx, from, to)
	if len(r.Failing) != 0 || len(r.Events) != 1 {
		t.Fatalf("after invalidate: %+v", r)
	}
}

// TestRealFixtures parses trimmed responses captured from Radarr 6.4 and
// Sonarr 4.0.20.
func TestRealFixtures(t *testing.T) {
	var movies []arr.Movie
	var eps []arr.Episode
	readJSON(t, "testdata/radarr-v6-calendar.json", &movies)
	readJSON(t, "testdata/sonarr-v4-calendar.json", &eps)
	var all []Event
	for _, m := range movies {
		ev := fromMovie(1, "Radarr", m)
		if len(ev) == 0 {
			t.Errorf("movie %q: no event", m.Title)
		}
		all = append(all, ev...)
	}
	for _, e := range eps {
		ev, ok := fromEpisode(2, "Sonarr", e)
		if !ok || ev.Air == nil || ev.Poster == "" || len(ev.Keys) == 0 {
			t.Errorf("episode %q: %+v", e.Title, ev)
		}
		all = append(all, ev)
	}
	for _, e := range merge(all) {
		if e.Date == "" || e.Title == "" || e.ID == "" {
			t.Errorf("incomplete event %+v", e)
		}
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
