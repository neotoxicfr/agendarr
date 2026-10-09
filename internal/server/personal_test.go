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
		j(w, `[{"id":1,"title":"Requested Movie","tmdbId":100,"digitalRelease":"2026-10-02T00:00:00Z","monitored":true,"titleSlug":"100","hasFile":true,
		        "movieFile":{"size":9,"quality":{"quality":{"name":"Bluray-1080p"}},"mediaInfo":{"audioCodec":"DTS","audioChannels":5.1,"audioLanguages":"fre","resolution":"1920x800","subtitles":"eng"}}},
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
	mux.HandleFunc("GET /plex/library/sections", func(w http.ResponseWriter, r *http.Request) {
		j(w, `{"MediaContainer":{"Directory":[{"key":"1","type":"movie"},{"key":"2","type":"show"},{"key":"9","type":"artist"}]}}`)
	})
	mux.HandleFunc("GET /plex/library/sections/1/all", func(w http.ResponseWriter, r *http.Request) {
		j(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"55","guid":"plex://movie/x","Guid":[{"id":"tmdb://100"}]}]}}`)
	})
	mux.HandleFunc("GET /plex/library/sections/2/all", func(w http.ResponseWriter, r *http.Request) {
		// The show shares TMDB id 100 with the movie: it must not take its Plex link (SEC-05).
		j(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"2199","Guid":[{"id":"tvdb://73871"},{"id":"tmdb://100"}]}]}}`)
	})
	mux.HandleFunc("GET /plex/library/metadata/2199/allLeaves", func(w http.ResponseWriter, r *http.Request) {
		j(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"301","parentIndex":1,"index":2},{"ratingKey":"302","parentIndex":1,"index":3}]}}`)
	})
	mux.HandleFunc("GET /plex/library/metadata/301", func(w http.ResponseWriter, r *http.Request) {
		j(w, `{"MediaContainer":{"Metadata":[{"Media":[{"videoResolution":"1080","Part":[{"size":5,"Stream":[{"streamType":1,"codec":"h264"},{"streamType":2,"codec":"aac","languageTag":"ja"}]}]}]}]}}`)
	})
	mux.HandleFunc("GET /plex/library/metadata/55", func(w http.ResponseWriter, r *http.Request) {
		j(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"55","Media":[{"videoResolution":"4k","Part":[{"size":6632526039,"Stream":[
		  {"streamType":1,"codec":"hevc","bitDepth":10,"DOVIPresent":true,"colorTrc":"smpte2084","displayTitle":"4K DoVi/HDR10+"},
		  {"streamType":2,"codec":"eac3","profile":"dolby digital plus + dolby atmos","languageTag":"fr","title":"FR VFF","audioChannelLayout":"5.1(side)","default":true},
		  {"streamType":2,"codec":"truehd","languageTag":"en-US","title":"ENG VO","audioChannelLayout":"7.1"},
		  {"streamType":3,"codec":"srt","languageTag":"fr","title":"FR Forced","forced":true},
		  {"streamType":3,"codec":"pgs","languageTag":"en","hearingImpaired":true},
		  {"streamType":3,"codec":"srt","languageTag":"fr","key":"/library/streams/9"}]}]}]}]}}`)
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

	// The old "hide available" checkbox becomes the "upcoming" status;
	// unknown statuses fall back to "all". In order: each save is partial.
	for _, c := range []struct{ body, want string }{
		{`{"filters":{"status":"","hideDownloaded":true}}`, "upcoming"},
		{`{"filters":{"status":"hack"}}`, "all"},
		{`{"filters":{"status":"available"}}`, "available"},
		{`{"view":"week"}`, "available"}, // another field: the status stays
	} {
		body, want := c.body, c.want
		req, _ := http.NewRequest("PUT", h.http.URL+"/api/prefs", strings.NewReader(body))
		req.Header.Set("Origin", h.http.URL)
		res, err := friend.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var got Prefs
		json.NewDecoder(res.Body).Decode(&got)
		res.Body.Close()
		if got.Filters.Status != want || got.Filters.HideDownloaded {
			t.Fatalf("%s: %+v", body, got.Filters)
		}
	}

	// "See on" sites: defaults until chosen, unknown ones dropped, none
	// allowed, kept by a save that does not send them.
	for _, c := range []struct{ body, want string }{
		{`{}`, "tmdb,imdb"},
		{`{"sites":["imdb","trakt","imdb","letterboxd"]}`, "imdb,letterboxd"},
		{`{"filters":{"movies":true}}`, "imdb,letterboxd"},
		{`{"sites":[]}`, ""},
	} {
		body, want := c.body, c.want
		req, _ := http.NewRequest("PUT", h.http.URL+"/api/prefs", strings.NewReader(body))
		req.Header.Set("Origin", h.http.URL)
		res, err := friend.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var got Prefs
		json.NewDecoder(res.Body).Decode(&got)
		res.Body.Close()
		if strings.Join(got.Sites, ",") != want || got.Sites == nil || got.Formats != "logos" {
			t.Fatalf("%s: %#v %q", body, got.Sites, got.Formats)
		}
	}
}

// Media details reach everyone; the Plex link too; Radarr links only admins,
// and only for instances with a public address.
func TestEventLinksAndMedia(t *testing.T) {
	up := fakeUpstream(t)
	h := newHarness(t, config.Config{DataDir: t.TempDir(), PlexServerURL: up.URL + "/plex"})
	owner, _ := h.signIn("owner")
	friend, _ := h.signIn("friend")
	ctx := context.Background()
	i := store.Instance{Kind: "radarr", Name: "Radarr", URL: up.URL + "/radarr", PublicURL: "https://radarr.example.com", Enabled: true}
	i.APIKey = h.srv.box.Seal("k")
	if _, err := h.srv.store.SaveInstance(ctx, i); err != nil {
		t.Fatal(err)
	}
	h.srv.instancesChanged()
	type ev struct {
		Title    string
		PlexURL  string
		ArrLinks []struct{ Label, URL string }
		Media    []struct {
			Resolution, AudioCodec, Channels string
			AudioLangs, Subs                 []string
		}
	}
	get := func(c *http.Client) ev {
		res, err := c.Get(h.http.URL + "/api/calendar?from=2026-10-01&to=2026-10-31")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body struct{ Events []ev }
		json.NewDecoder(res.Body).Decode(&body)
		for _, e := range body.Events {
			if e.Title == "Requested Movie" {
				return e
			}
		}
		t.Fatalf("movie missing: %+v", body.Events)
		return ev{}
	}
	// The Plex index is built in the background after the first request.
	var o ev
	for n := 0; n < 50; n++ {
		if o = get(owner); o.PlexURL != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(o.PlexURL, "/server/A/details?key=%2Flibrary%2Fmetadata%2F55") {
		t.Fatalf("plex url: %q", o.PlexURL)
	}
	if len(o.ArrLinks) != 1 || o.ArrLinks[0].URL != "https://radarr.example.com/movie/100" {
		t.Fatalf("admin links: %+v", o.ArrLinks)
	}
	m := o.Media
	if len(m) != 1 || m[0].Resolution != "1080p" || m[0].AudioCodec != "DTS" || m[0].Channels != "5.1" ||
		strings.Join(m[0].AudioLangs, ",") != "fr" || strings.Join(m[0].Subs, ",") != "en" {
		t.Fatalf("media: %+v", m)
	}
	f := get(friend)
	if f.PlexURL == "" || len(f.ArrLinks) != 0 || len(f.Media) != 1 {
		t.Fatalf("friend: %+v", f)
	}

	// Tracks come from Plex, per track; unknown items answer an empty list.
	tracks := func(q string) string {
		res, err := friend.Get(h.http.URL + "/api/tracks?" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return strings.TrimSpace(string(b))
	}
	want := `{"versions":[{"resolution":"4K","videoCodec":"hevc","bitDepth":10,"hdr":["DV","HDR10+"],"size":6632526039,` +
		`"audio":[{"codec":"eac3","profile":"dolby digital plus + dolby atmos","lang":"fr","title":"FR VFF","layout":"5.1","default":true},` +
		`{"codec":"truehd","lang":"en-US","title":"ENG VO","layout":"7.1"}],` +
		`"subs":[{"codec":"srt","lang":"fr","title":"FR Forced","forced":true},{"codec":"pgs","lang":"en","hi":true},` +
		`{"codec":"srt","lang":"fr","external":true}]}]}`
	if got := tracks("group=movie:tmdb:100"); got != want {
		t.Fatalf("tracks:\n got %s\nwant %s", got, want)
	}
	if got := tracks("group=movie:tmdb:999"); got != `{"versions":[]}` {
		t.Fatalf("unknown: %s", got)
	}
	// A season bundle at once: E02 is in Plex, E03 is listed but has no
	// metadata, E09 is not in Plex.
	if got := tracks("group=series:tvdb:73871&season=1&episodes=2,3,9"); got != `{"episodes":{"2":[{"resolution":"1080p","videoCodec":"h264","size":5,"audio":[{"codec":"aac","lang":"ja"}],"subs":null}]}}` {
		t.Fatalf("season: %s", got)
	}
}
