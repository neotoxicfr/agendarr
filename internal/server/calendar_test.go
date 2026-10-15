package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/neotoxicfr/agendarr/internal/config"
	"github.com/neotoxicfr/agendarr/internal/store"
)

func TestCalendarAPIAndPosters(t *testing.T) {
	var posterHits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v3/calendar":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[{"id":5,"title":"Dune","tmdbId":42,"digitalRelease":"2026-10-02T00:00:00Z","monitored":true,
				"images":[{"coverType":"poster","remoteUrl":"https://image.tmdb.org/x.jpg"}]}]`)
		case "/api/v3/mediacover/5/poster-500.jpg":
			posterHits.Add(1)
			w.Header().Set("Content-Type", "image/jpeg")
			io.WriteString(w, "JPEG")
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()

	h := newHarness(t, config.Config{DataDir: t.TempDir()})
	owner, _ := h.signIn("owner")
	ctx := context.Background()
	if _, err := h.srv.store.SaveInstance(ctx, store.Instance{Kind: "radarr", Name: "Radarr", URL: up.URL, APIKey: h.srv.box.Seal("k"), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	h.srv.instancesChanged()

	get := func(c *http.Client, path string) (*http.Response, []byte) {
		res, err := c.Get(h.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}
	if res, _ := get(owner, "/api/calendar?from=2026-10-01&to=2026-12-31"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("range too wide: %d", res.StatusCode)
	}
	res, body := get(owner, "/api/calendar?from=2026-10-01&to=2026-10-31")
	var cal struct {
		Events []struct {
			Title, Release, Poster string
		}
		Configured bool
	}
	if err := json.Unmarshal(body, &cal); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("calendar: %d %s", res.StatusCode, body)
	}
	if !cal.Configured || len(cal.Events) != 1 || cal.Events[0].Release != "digital" || cal.Events[0].Poster == "" {
		t.Fatalf("calendar body: %+v", cal)
	}

	// Posters: proxied with the key, cached on disk, second hit not upstream.
	for i := 0; i < 2; i++ {
		res, body = get(owner, cal.Events[0].Poster+"?w=500")
		if res.StatusCode != http.StatusOK || string(body) != "JPEG" || res.Header.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("poster: %d %q", res.StatusCode, body)
		}
	}
	if posterHits.Load() != 1 {
		t.Fatalf("poster fetched %d times upstream", posterHits.Load())
	}
	if _, err := os.Stat(filepath.Join(h.srv.cfg.DataDir, "cache", "posters", "1-5-500.jpg")); err != nil {
		t.Fatalf("poster cache: %v", err)
	}
	if res, _ = get(owner, "/img/1/99"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing poster: %d", res.StatusCode)
	}
	// Anonymous: no calendar, no posters.
	anon := &http.Client{}
	if res, _ = get(anon, "/api/calendar?from=2026-10-01&to=2026-10-31"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous api: %d", res.StatusCode)
	}
}
