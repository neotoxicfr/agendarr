package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neotoxicfr/agendarr/internal/config"
	"github.com/neotoxicfr/agendarr/internal/store"
)

func TestReleaseWatcher(t *testing.T) {
	var mu sync.Mutex
	movie := `{"id":1,"title":"Requested Movie","tmdbId":100,"digitalRelease":"2026-10-02T00:00:00Z","monitored":true,"hasFile":false}`
	mux := http.NewServeMux()
	asJSON := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			next(w, r)
		}
	}
	mux.HandleFunc("GET /radarr/api/v3/calendar", asJSON(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		io.WriteString(w, "["+movie+"]")
	}))
	mux.HandleFunc("GET /seerr/api/v1/user", asJSON(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"pageInfo":{"results":2},"results":[{"id":1,"plexId":1,"displayName":"neo"},{"id":2,"plexId":2,"displayName":"Friend"}]}`)
	}))
	mux.HandleFunc("GET /seerr/api/v1/request", asJSON(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"pageInfo":{"results":1},"results":[{"id":1,"type":"movie","requestedBy":{"id":2},"media":{"tmdbId":100}}]}`)
	}))
	up := httptest.NewServer(mux)
	defer up.Close()

	h := newHarness(t, config.Config{DataDir: t.TempDir()})
	owner, _ := h.signIn("owner")
	friend, _ := h.signIn("friend")
	ctx := context.Background()
	for _, i := range []store.Instance{
		{Kind: "radarr", Name: "Radarr", URL: up.URL + "/radarr"},
		{Kind: "seerr", Name: "Seerr", URL: up.URL + "/seerr"},
	} {
		i.APIKey, i.Enabled = h.srv.box.Seal("k"), true
		if _, err := h.srv.store.SaveInstance(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	h.srv.instancesChanged()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	check := func(file string) {
		t.Helper()
		mu.Lock()
		movie = strings.Replace(movie, movie[strings.Index(movie, `"hasFile"`):], file+"}", 1)
		mu.Unlock()
		h.srv.instancesChanged() // drop the calendar cache
		if err := h.srv.checkReleases(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	type item struct {
		ID      int64
		Kind    string
		Title   string
		Detail  string
		Read    bool
		Follows bool
	}
	list := func(c *http.Client) ([]item, int) {
		t.Helper()
		res, err := c.Get(h.http.URL + "/api/notifications")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body struct {
			Items    []item
			Unread   int
			VapidKey string
		}
		json.NewDecoder(res.Body).Decode(&body)
		if body.VapidKey == "" {
			t.Fatal("no VAPID key")
		}
		return body.Items, body.Unread
	}
	send := func(c *http.Client, method, path, body string) int {
		t.Helper()
		req, _ := http.NewRequest(method, h.http.URL+path, strings.NewReader(body))
		req.Header.Set("Origin", h.http.URL)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// First sight: remembered, nothing said.
	check(`"hasFile":false`)
	if l, _ := list(friend); len(l) != 0 {
		t.Fatalf("first run: %+v", l)
	}
	// Available: the requester hears of it, not the owner.
	check(`"hasFile":true,"movieFile":{"size":1,"mediaInfo":{"resolution":"1920x1080","videoCodec":"x264","audioCodec":"AAC","audioChannels":2,"audioLanguages":"eng"}}`)
	l, unread := list(friend)
	if len(l) != 1 || l[0].Kind != "available" || l[0].Title != "Requested Movie" || unread != 1 {
		t.Fatalf("available: %+v %d", l, unread)
	}
	if o, _ := list(owner); len(o) != 0 {
		t.Fatalf("owner told: %+v", o)
	}
	// Nothing changed: nothing new.
	check(`"hasFile":true,"movieFile":{"size":1,"mediaInfo":{"resolution":"1920x1080","videoCodec":"x264","audioCodec":"AAC","audioChannels":2,"audioLanguages":"eng"}}`)
	if l, _ := list(friend); len(l) != 1 {
		t.Fatalf("repeat: %+v", l)
	}
	// The owner follows it by hand; then an upgrade reaches both, with what changed.
	if code := send(owner, "PUT", "/api/follows", `{"group":"movie:tmdb:100","title":"Requested Movie","on":true}`); code != 204 {
		t.Fatalf("follow: %d", code)
	}
	if code := send(owner, "PUT", "/api/follows", `{"group":"../etc","on":true}`); code != 400 {
		t.Fatalf("bad group: %d", code)
	}
	check(`"hasFile":true,"movieFile":{"size":2,"mediaInfo":{"resolution":"3840x2160","videoCodec":"x265","videoDynamicRangeType":"DV","audioCodec":"EAC3","audioChannels":5.1,"audioLanguages":"eng/fre"}}`)
	o, _ := list(owner)
	if len(o) != 1 || o[0].Kind != "upgraded" || !o[0].Follows {
		t.Fatalf("owner upgrade: %+v", o)
	}
	for _, want := range []string{`"what":"resolution","from":"1080p","to":"4K"`, `"what":"hdr","to":"DV"`, `"what":"audioLangs","add":["fr"]`} {
		if !strings.Contains(o[0].Detail, want) {
			t.Fatalf("detail %s lacks %s", o[0].Detail, want)
		}
	}
	if l, unread := list(friend); len(l) != 2 || unread != 2 {
		t.Fatalf("friend upgrade: %+v", l)
	}
	// Acknowledge one, then all.
	if code := send(friend, "POST", "/api/notifications/read", `{"ids":[`+jsonInt(l[0].ID)+`]}`); code != 204 {
		t.Fatalf("read: %d", code)
	}
	if _, unread := list(friend); unread != 1 {
		t.Fatalf("after one: %d", unread)
	}
	send(friend, "POST", "/api/notifications/read", `{}`)
	if _, unread := list(friend); unread != 0 {
		t.Fatalf("after all: %d", unread)
	}
	// Someone else's notifications cannot be acknowledged.
	send(owner, "POST", "/api/notifications/read", `{"ids":[`+jsonInt(l[0].ID)+`]}`)
	// Sources picked in the bell survive a calendar save of the filters.
	if code := send(friend, "PUT", "/api/notifications/sources", `{"notify":["watching"]}`); code != 200 {
		t.Fatalf("sources: %d", code)
	}
	send(friend, "PUT", "/api/prefs", `{"mine":[],"filters":{"movies":true,"episodes":true}}`)
	if got := h.srv.loadPrefs(ctx, mustUser(t, h, 2).ID).Notify; strings.Join(got, ",") != "watching" {
		t.Fatalf("notify after prefs save: %v", got)
	}
	// A push subscription on an unknown host is refused.
	if code := send(friend, "POST", "/api/push", `{"subscription":{"endpoint":"https://evil.example/x","keys":{"p256dh":"a","auth":"b"}}}`); code != 400 {
		t.Fatalf("evil push host: %d", code)
	}
}

func jsonInt(n int64) string { return strconv.FormatInt(n, 10) }

func TestPushBody(t *testing.T) {
	h := newHarness(t, config.Config{DataDir: t.TempDir()})
	c := &change{kind: "upgraded"}
	d := notifyDetail{Episodes: []epRef{{6, 4, "Lost & Found"}}, Diffs: []diff{{What: "resolution", From: "1080p", To: "4K"}}}
	if got := h.srv.pushBody("fr", c, d); got != "S06E04 · Lost & Found — Nouvelle version : 1080p → 4K" {
		t.Fatalf("fr: %q", got)
	}
	if got := h.srv.pushBody("en", c, d); got != "S06E04 · Lost & Found — New version: 1080p → 4K" {
		t.Fatalf("en: %q", got)
	}
	c.kind = "available"
	d = notifyDetail{Episodes: []epRef{{1, 1, ""}, {1, 2, ""}, {1, 6, ""}}}
	if got := h.srv.pushBody("en", c, d); got != "S01E01–S01E06 (3 episodes) — Available" {
		t.Fatalf("en: %q", got)
	}
	if got := h.srv.pushBody("en", c, notifyDetail{}); got != "Available" {
		t.Fatalf("movie: %q", got)
	}
}
