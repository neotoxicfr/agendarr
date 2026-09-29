package arr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neotoxicfr/agendarr/internal/httpx"
)

func TestCalendars(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /base/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"appName":"Radarr","version":"5.20.1"}`))
	})
	mux.HandleFunc("GET /base/api/v3/calendar", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.URL.Query().Get("unmonitored") != "true" || r.URL.Query().Get("start") == "" {
			w.WriteHeader(400)
			return
		}
		if r.URL.Query().Get("includeSeries") == "true" {
			w.Write([]byte(`[{"id":9,"seriesId":3,"seasonNumber":2,"episodeNumber":5,"title":"Ep","airDate":"2026-10-02",
			  "airDateUtc":"2026-10-03T01:00:00Z","hasFile":false,"monitored":true,
			  "series":{"id":3,"title":"Show","tvdbId":123,"tmdbId":456,"network":"HBO","runtime":55,
			    "images":[{"coverType":"banner","url":"/b.jpg"},{"coverType":"poster","url":"/MediaCover/3/poster.jpg?lastWrite=1"}]}}]`))
			return
		}
		w.Write([]byte(`[{"id":1,"title":"Film","year":2026,"tmdbId":42,"imdbId":"tt1","inCinemas":"2026-10-01T00:00:00Z",
		  "digitalRelease":null,"hasFile":true,"monitored":true,"images":[]}]`))
	})
	mux.HandleFunc("GET /html/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>login</html>"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ctx := context.Background()

	c := New(ts.URL+"/base/", "k")
	if s, err := c.Status(ctx); err != nil || s.AppName != "Radarr" {
		t.Fatalf("status: %+v %v", s, err)
	}
	if _, err := New(ts.URL+"/base", "bad").Status(ctx); !errors.Is(err, httpx.ErrUnauthorized) {
		t.Fatalf("bad key: %v", err)
	}
	if _, err := New(ts.URL+"/html", "k").Status(ctx); err == nil {
		t.Fatal("HTML page accepted as JSON")
	}
	now := time.Now()
	mv, err := c.Movies(ctx, now, now.Add(time.Hour))
	if err != nil || len(mv) != 1 || mv[0].InCinemas == nil || mv[0].DigitalRelease != nil || !mv[0].HasFile {
		t.Fatalf("movies: %+v %v", mv, err)
	}
	ep, err := c.Episodes(ctx, now, now.Add(time.Hour))
	if err != nil || len(ep) != 1 || ep[0].Series.TvdbID != 123 || !HasPoster(ep[0].Series.Images) {
		t.Fatalf("episodes: %+v %v", ep, err)
	}
}
