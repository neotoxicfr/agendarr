package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neotoxicfr/agendarr/internal/config"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// One fake upstream plays Radarr, Sonarr, Seerr and the Plex server.
func fakeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	j := func(w http.ResponseWriter, s string) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, s)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /radarr/api/v3/calendar", func(w http.ResponseWriter, r *http.Request) {
		j(w, `[{"id":1,"title":"Requested Movie","tmdbId":100,"digitalRelease":"2026-10-02T00:00:00Z","monitored":true},
		       {"id":2,"title":"Other Movie","tmdbId":200,"digitalRelease":"2026-10-03T00:00:00Z","monitored":true}]`)
	})
	mux.HandleFunc("GET /sonarr/api/v3/calendar", func(w http.ResponseWriter, r *http.Request) {
		j(w, `[{"id":9,"seasonNumber":1,"episodeNumber":2,"airDateUtc":"2026-10-04T20:00:00Z","monitored":true,
		        "series":{"id":3,"title":"Watched Show","tvdbId":73871,"tmdbId":615}},
		       {"id":10,"seasonNumber":1,"episodeNumber":1,"airDateUtc":"2026-10-05T20:00:00Z","monitored":true,
		        "series":{"id":4,"title":"Unknown Show","tvdbId":5555}}]`)
	})
	mux.HandleFunc("GET /seerr/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		j(w, `{"pageInfo":{"results":2},"results":[{"id":1,"plexId":1,"displayName":"neo"},{"id":2,"plexId":2,"displayName":"Friend"}]}`)
	})
	mux.HandleFunc("GET /seerr/api/v1/request", func(w http.ResponseWriter, r *http.Request) {
		j(w, `{"pageInfo":{"results":1},"results":[{"id":1,"type":"movie","requestedBy":{"id":2},"media":{"tmdbId":100}}]}`)
	})
	mux.HandleFunc("GET /plex/status/sessions/history/all", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("accountID") != "2" {
			j(w, `{"MediaContainer":{"totalSize":0}}`)
			return
		}
		j(w, `{"MediaContainer":{"totalSize":1,"Metadata":[{"type":"episode","ratingKey":"7","grandparentKey":"/library/metadata/2199","viewedAt":1790000000}]}}`)
	})
	mux.HandleFunc("GET /plex/library/metadata/2199", func(w http.ResponseWriter, r *http.Request) {
		j(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"2199","guid":"plex://show/x","Guid":[{"id":"tvdb://73871"}]}]}}`)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestPersonalFilters(t *testing.T) {
	up := fakeUpstream(t)
	h := newHarness(t, config.Config{DataDir: t.TempDir(), PlexServerURL: up.URL + "/plex"})
	h.signIn("owner")
	friend, _ := h.signIn("friend")
	ctx := context.Background()
	for _, i := range []store.Instance{
		{Kind: "radarr", Name: "Radarr", URL: up.URL + "/radarr"},
		{Kind: "sonarr", Name: "Sonarr", URL: up.URL + "/sonarr"},
		{Kind: "seerr", Name: "Seerr", URL: up.URL + "/seerr"},
	} {
		i.APIKey, i.Enabled = h.srv.box.Seal("k"), true
		if _, err := h.srv.store.SaveInstance(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	h.srv.instancesChanged()
	titles := func(q string) ([]string, map[string]string) {
		res, err := friend.Get(h.http.URL + "/api/calendar?from=2026-10-01&to=2026-10-31" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body struct {
			Events []struct {
				Title       string
				RequestedBy []string
			}
			Personal map[string]string
		}
		json.NewDecoder(res.Body).Decode(&body)
		var out []string
		for _, e := range body.Events {
			out = append(out, e.Title+"|"+strings.Join(e.RequestedBy, ","))
		}
		return out, body.Personal
	}
	all, _ := titles("")
	if len(all) != 4 || all[0] != "Requested Movie|Friend" {
		t.Fatalf("all: %v", all)
	}
	got, errs := titles("&mine=seerr")
	if strings.Join(got, ";") != "Requested Movie|Friend" || len(errs) != 0 {
		t.Fatalf("seerr: %v %v", got, errs)
	}
	got, _ = titles("&mine=watching")
	if strings.Join(got, ";") != "Watched Show|" {
		t.Fatalf("watching: %v", got)
	}
	got, _ = titles("&mine=seerr,watching,bogus")
	if len(got) != 2 {
		t.Fatalf("union: %v", got)
	}
	// After a failed sync, the next request waits for the retry instead of
	// serving the empty set with the old error.
	u, err := h.srv.store.UserByPlexID(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.srv.store.SetUserMedia(ctx, u.ID, "watched", nil, errors.New("boom"), time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, errs = titles("&mine=watched")
	if strings.Join(got, ";") != "Watched Show|" || len(errs) != 0 {
		t.Fatalf("retry after failure: %v %v", got, errs)
	}
	// The history is cached in plex_guids: a second user sync reuses it.
	if g, _ := h.srv.store.PlexGuids(ctx, []string{"2199"}); len(g["2199"]) != 1 {
		t.Fatalf("guid cache: %v", g)
	}

	// Preferences round-trip and are sanitised.
	req, _ := http.NewRequest("PUT", h.http.URL+"/api/prefs", strings.NewReader(`{"view":"hack","mine":["seerr","x","seerr"],"filters":{"movies":false,"episodes":true}}`))
	req.Header.Set("Origin", h.http.URL)
	res, err := friend.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	res, _ = friend.Get(h.http.URL + "/api/prefs")
	var p Prefs
	json.NewDecoder(res.Body).Decode(&p)
	res.Body.Close()
	if p.View != "" || strings.Join(p.Mine, ",") != "seerr" || p.Filters.Movies || !p.Filters.Episodes {
		t.Fatalf("prefs: %+v", p)
	}
}
