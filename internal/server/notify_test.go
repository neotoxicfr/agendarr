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
	down := false
	mux.HandleFunc("GET /radarr/api/v3/calendar", asJSON(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if down {
			http.Error(w, "down", 500)
			return
		}
		io.WriteString(w, "["+movie+"]")
	}))
	mux.HandleFunc("GET /seerr/api/v1/user", asJSON(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"pageInfo":{"results":2},"results":[{"id":1,"plexId":1,"displayName":"neo"},{"id":2,"plexId":2,"displayName":"Friend"}]}`)
	}))
	gate := make(chan struct{}) // a slow Seerr, opened by the test
	close(gate)
	var gateMu sync.Mutex
	mux.HandleFunc("GET /seerr/api/v1/request", asJSON(func(w http.ResponseWriter, r *http.Request) {
		gateMu.Lock()
		g := gate
		gateMu.Unlock()
		<-g
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
		now = now.Add(watchEvery)
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
	// Available while Seerr's index is not built yet: the watcher does not
	// wait (PERF-01), the change waits for the next check (SEC-04).
	h.srv.personal = h.srv.newPersonal()
	gateMu.Lock()
	gate = make(chan struct{})
	gateMu.Unlock()
	check(`"hasFile":true,"movieFile":{"size":1,"mediaInfo":{"resolution":"1920x1080","videoCodec":"x264","audioCodec":"AAC","audioChannels":2,"audioLanguages":"eng"}}`)
	if l, _ := list(friend); len(l) != 0 {
		t.Fatalf("delivered before the sources were ready: %+v", l)
	}
	gateMu.Lock()
	close(gate)
	gateMu.Unlock()
	for n := 0; n < 50; n++ { // the background index build
		if h.srv.personal.RequestIndex(ctx) != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
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
	// SEC-01: renaming the instance, or Radarr failing then coming back,
	// says nothing.
	insts, _ := h.srv.store.Instances(ctx)
	for _, i := range insts {
		if i.Kind == "radarr" {
			i.Name = "Radarr HD"
			h.srv.store.SaveInstance(ctx, i)
		}
	}
	same := `"hasFile":true,"movieFile":{"size":2,"mediaInfo":{"resolution":"3840x2160","videoCodec":"x265","videoDynamicRangeType":"DV","audioCodec":"EAC3","audioChannels":5.1,"audioLanguages":"eng/fre"}}`
	check(same)
	mu.Lock()
	down = true
	mu.Unlock()
	check(same)
	mu.Lock()
	down = false
	mu.Unlock()
	check(same)
	if l, _ := list(friend); len(l) != 2 {
		t.Fatalf("rename/outage notified: %+v", l)
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

// SEC-01: only a file that changed on the same instance is an upgrade.
func TestUpgradeDiffs(t *testing.T) {
	a := "1|1080p||x264|AAC|2.0|en|"
	b := "1|2160p|DV|x265|EAC3|5.1|en+fr|"
	if d := upgradeDiffs(a, b); len(d) == 0 || d[0].What != "resolution" || d[0].To != "2160p" {
		t.Fatalf("same instance: %+v", d)
	}
	if d := upgradeDiffs(a, a+"\n2|2160p||x265|EAC3|5.1|en|"); d != nil {
		t.Fatalf("instance added: %+v", d)
	}
	if d := upgradeDiffs("Radarr|1080p||x264|AAC|2.0|en|", a); d != nil {
		t.Fatalf("old name-keyed signature: %+v", d)
	}
}

// Each person hears only about the kinds they picked.
func TestWantsKinds(t *testing.T) {
	avail := &change{kind: "available"}
	quality := &change{kind: "upgraded", diffs: []diff{{What: "resolution", From: "1080p", To: "4K"}}}
	audio := &change{kind: "upgraded", diffs: []diff{{What: "audioLangs", Add: []string{"fr"}}}}
	subs := &change{kind: "upgraded", diffs: []diff{{What: "subs", Add: []string{"en"}}}}
	for _, c := range []struct {
		kinds []string
		ch    *change
		want  bool
	}{
		{cleanKinds(nil), quality, true},
		{[]string{"available"}, avail, true},
		{[]string{"available"}, quality, false},
		{[]string{"audio"}, audio, true},
		{[]string{"audio"}, subs, false},
		{[]string{"subs", "quality"}, subs, true},
		{[]string{}, avail, false},
	} {
		if got := wants(c.kinds, c.ch); got != c.want {
			t.Fatalf("%v on %+v: %v", c.kinds, c.ch, got)
		}
	}
}

// Notifications are deleted one by one or all at once, never someone else's.
func TestDeleteNotifications(t *testing.T) {
	h := newHarness(t, config.Config{DataDir: t.TempDir()})
	owner, _ := h.signIn("owner")
	friend, _ := h.signIn("friend")
	ctx := context.Background()
	o, f := mustUser(t, h, 1), mustUser(t, h, 2)
	var ids []int64
	for _, u := range []*store.User{o, o, o, f} {
		id, err := h.srv.store.AddNotification(ctx, u.ID, store.Notification{At: time.Now(), Kind: "available", Group: "movie:tmdb:1", Title: "x", Detail: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	post := func(c *http.Client, body string) int {
		req, _ := http.NewRequest("POST", h.http.URL+"/api/notifications/delete", strings.NewReader(body))
		req.Header.Set("Origin", h.http.URL)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	count := func(u *store.User) int { l, _, _ := h.srv.store.Notifications(ctx, u.ID, 500); return len(l) }
	if post(owner, `{}`) != 400 {
		t.Fatal("an empty body must not delete everything")
	}
	post(owner, `{"ids":[`+jsonInt(ids[0])+`,`+jsonInt(ids[3])+`]}`) // ids[3] is the friend's
	if count(o) != 2 || count(f) != 1 {
		t.Fatalf("one by one: owner %d friend %d", count(o), count(f))
	}
	post(owner, `{"all":true}`)
	if count(o) != 0 || count(f) != 1 {
		t.Fatalf("all: owner %d friend %d", count(o), count(f))
	}
	_ = friend
}
