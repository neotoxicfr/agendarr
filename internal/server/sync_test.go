package server

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/neotoxicfr/agendarr/internal/calendar"
	"github.com/neotoxicfr/agendarr/internal/config"
	"github.com/neotoxicfr/agendarr/internal/store"
)

func TestICSFeed(t *testing.T) {
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
	res := h.form(friend, "/sync/new", nil)
	res.Body.Close()
	fu, err := h.srv.feedLinks(httptestRequest(h), mustUser(t, h, 2))
	if err != nil || fu == nil || !strings.HasPrefix(string(fu.Webcal), "webcal://") || !strings.Contains(fu.Google, "cid=webcal") {
		t.Fatalf("links: %+v %v", fu, err)
	}
	// The Apple Calendar button keeps its webcal:// link (html/template
	// would turn an unknown scheme into #ZgotmplZ).
	page, _ := friend.Get(h.http.URL + "/sync")
	pb, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if !strings.Contains(string(pb), `href="webcal://`) {
		t.Fatal("webcal link lost in the sync page")
	}
	// The database holds the hash, never the token.
	token := strings.TrimSuffix(strings.SplitN(strings.TrimPrefix(fu.HTTPS, h.http.URL+"/ics/"), "?", 2)[0], ".ics")
	var n int
	h.srv.store.DB.QueryRow(`SELECT count(*) FROM ics_tokens WHERE token = ?`, token).Scan(&n)
	if n != 0 {
		t.Fatal("token stored in clear")
	}

	anon := &http.Client{}
	get := func(u string) (int, string) {
		res, err := anon.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := get(fu.HTTPS)
	if code != 200 || !strings.HasPrefix(body, "BEGIN:VCALENDAR\r\n") {
		t.Fatalf("feed: %d %.200s", code, body)
	}
	// Prefs apply: movies off leaves no movie in the feed.
	req, _ := http.NewRequest("PUT", h.http.URL+"/api/prefs", strings.NewReader(`{"mine":[],"filters":{"movies":false,"episodes":true}}`))
	req.Header.Set("Origin", h.http.URL)
	r2, _ := friend.Do(req)
	r2.Body.Close()
	_, body = get(fu.HTTPS)
	if strings.Contains(body, "CATEGORIES:Movie") {
		t.Fatal("movie in feed despite filters")
	}
	if code, _ := get(h.http.URL + "/ics/" + strings.Repeat("x", 43) + ".ics"); code != 404 {
		t.Fatalf("unknown token: %d", code)
	}
	res = h.form(friend, "/sync/revoke", url.Values{})
	res.Body.Close()
	if code, _ := get(fu.HTTPS); code != 404 {
		t.Fatalf("revoked feed: %d", code)
	}
}

func httptestRequest(h *harness) *http.Request {
	r, _ := http.NewRequest("GET", h.http.URL+"/sync", nil)
	r.Host = strings.TrimPrefix(h.http.URL, "http://")
	return r
}

func mustUser(t *testing.T, h *harness, plexID int64) *store.User {
	t.Helper()
	u, err := h.srv.store.UserByPlexID(context.Background(), plexID)
	if err != nil {
		t.Fatal(err)
	}
	return &u
}

func TestFilterEventsStatus(t *testing.T) {
	in := []calendar.Event{{Kind: "movie", Title: "on disk", HasFile: true, Monitored: true}, {Kind: "movie", Title: "coming", Monitored: true}}
	for status, want := range map[string]string{"all": "on disk,coming", "available": "on disk", "upcoming": "coming"} {
		var got []string
		for _, e := range filterEvents(in, PrefFilters{Movies: true, Episodes: true, Status: status}) {
			got = append(got, e.Title)
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("%s: %v", status, got)
		}
	}
}
