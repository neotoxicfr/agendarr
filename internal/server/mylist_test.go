package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neotoxicfr/agendarr/internal/config"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// A library build where one instance failed does not hold for 30 minutes
// (EDGE-a4-04), and changing instances drops the library and poster caches
// (EDGE-a4-04, EDGE-a4-05).
func TestCatalogPartialAndInstancesChanged(t *testing.T) {
	var downB atomic.Bool
	mux := http.NewServeMux()
	lib := func(id string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if id == "2" && downB.Load() {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[{"id":`+id+`,"title":"M`+id+`","tmdbId":`+id+`}]`)
		}
	}
	mux.HandleFunc("GET /a/api/v3/movie", lib("1"))
	mux.HandleFunc("GET /b/api/v3/movie", lib("2"))
	up := httptest.NewServer(mux)
	defer up.Close()
	h := newHarness(t, config.Config{DataDir: t.TempDir()})
	ctx := context.Background()
	for _, i := range []store.Instance{{Kind: "radarr", Name: "A", URL: up.URL + "/a"}, {Kind: "radarr", Name: "B", URL: up.URL + "/b"}} {
		i.APIKey, i.Enabled = h.srv.box.Seal("k"), true
		if _, err := h.srv.store.SaveInstance(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(h.srv.catalogItems(ctx)); n != 2 {
		t.Fatalf("complete build: %d items", n)
	}
	// Stale, B down: the partial build must leave the retry to the backoff.
	c := &h.srv.catalog
	c.mu.Lock()
	c.at, c.tried = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
	c.mu.Unlock()
	downB.Store(true)
	h.srv.catalogItems(ctx)
	for n := 0; n < 100; n++ {
		c.mu.Lock()
		running := c.running
		c.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	age := time.Since(c.at)
	c.mu.Unlock()
	if age < catalogTTL {
		t.Fatalf("a partial build was kept as fresh for %v", catalogTTL-age)
	}

	// Poster cache and library cache go with an instance change.
	posters := filepath.Join(h.srv.cfg.DataDir, "cache", "posters")
	if err := os.MkdirAll(posters, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(posters, "1-1-250.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.at, c.tried = time.Now(), time.Now()
	c.mu.Unlock()
	h.srv.instancesChanged()
	c.mu.Lock()
	reset := c.at.IsZero() && c.tried.IsZero()
	c.mu.Unlock()
	if !reset {
		t.Fatal("library cache survived an instance change")
	}
	if _, err := os.Stat(posters); !os.IsNotExist(err) {
		t.Fatalf("poster cache survived an instance change: %v", err)
	}
}
