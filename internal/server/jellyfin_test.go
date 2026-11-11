package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neotoxicfr/agendarr/internal/config"
)

// fakeJellyfin is a Jellyfin server whose answers a test steers.
type fakeJellyfin struct {
	mu       sync.Mutex
	serverID string
	approved bool
	qcUser   string // who approves the Quick Connect code
	disabled map[string]bool
	logouts  int
	keys     int
	connects int    // Quick Connect polls that reached this server
	auths    int    // password sign-ins that reached this server
	publicID string // overrides the id /System/Info/Public reports
	keyFail  bool   // key creation is refused
	deadCode bool   // the Quick Connect code is unknown
}

var jfUsers = map[string]struct {
	id, password string
	admin        bool
}{
	"admin":  {"b53deadd670c44ad87d37783c299e530", "pw-admin", true},
	"friend": {"0534d2b624ff479cb4e2d05fded41061", "pw-friend", false},
}

func (f *fakeJellyfin) auth(name string) string {
	u := jfUsers[name]
	return fmt.Sprintf(`{"AccessToken":"tok-%s","ServerId":%q,"User":{"Id":%q,"Name":%q,"Policy":{"IsAdministrator":%t,"IsDisabled":%t}}}`,
		name, f.serverID, u.id, name, u.admin, f.disabled[name])
}

func (f *fakeJellyfin) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /System/Info/Public":
			id := f.serverID
			if f.publicID != "" {
				id = f.publicID
			}
			fmt.Fprintf(w, `{"Id":%q,"ServerName":"Home","Version":"12.4.0"}`, id)
		case "GET /QuickConnect/Enabled":
			io.WriteString(w, "true")
		case "POST /QuickConnect/Initiate":
			io.WriteString(w, `{"Code":"123456","Secret":"s3cret","Authenticated":false}`)
		case "GET /QuickConnect/Connect":
			f.connects++
			if f.deadCode {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"Code":"123456","Secret":"s3cret","Authenticated":%t}`, f.approved)
		case "POST /Users/AuthenticateWithQuickConnect":
			io.WriteString(w, f.auth(f.qcUser))
		case "POST /Users/AuthenticateByName":
			f.auths++
			var b struct{ Username, Pw string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			if u, ok := jfUsers[b.Username]; !ok || u.password != b.Pw {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, f.auth(b.Username))
		case "POST /Sessions/Logout":
			f.logouts++
			w.WriteHeader(http.StatusNoContent)
		case "POST /Auth/Keys":
			if f.keyFail || !strings.Contains(r.Header.Get("Authorization"), `Token="tok-admin"`) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			f.keys++
			w.WriteHeader(http.StatusNoContent)
		case "GET /Users":
			if !strings.Contains(r.Header.Get("Authorization"), `Token="the-key"`) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var out []string
			for name, u := range jfUsers {
				out = append(out, fmt.Sprintf(`{"Id":%q,"Name":%q,"Policy":{"IsAdministrator":%t,"IsDisabled":%t}}`, u.id, name, u.admin, f.disabled[name]))
			}
			io.WriteString(w, "["+strings.Join(out, ",")+"]")
		case "GET /System/Info":
			if !strings.Contains(r.Header.Get("Authorization"), `Token="the-key"`) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"Id":%q,"ServerName":"Home"}`, f.serverID)
		case "GET /Auth/Keys":
			io.WriteString(w, `{"Items":[{"AccessToken":"the-key","AppName":"Agendarr","DateCreated":"2026-11-04T00:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func newJellyfinHarness(t *testing.T) (*harness, *fakeJellyfin, *httptest.Server) {
	t.Helper()
	h := newHarness(t, config.Config{DataDir: t.TempDir()})
	f := &fakeJellyfin{serverID: "6d76258c55be4914b3d123ff4b13ddc5", qcUser: "admin", disabled: map[string]bool{}}
	jf := httptest.NewServer(f.handler())
	t.Cleanup(jf.Close)
	return h, f, jf
}

func (h *harness) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// redirectOf posts a form and returns where the app redirects.
func (h *harness) redirectOf(c *http.Client, path string, v url.Values) string {
	h.t.Helper()
	res := h.form(c, path, v)
	res.Body.Close()
	return res.Header.Get("Location")
}

func TestJellyfinFirstRunAndSignIn(t *testing.T) {
	h, f, jf := newJellyfinHarness(t)
	ctx := context.Background()
	owner := h.client()

	if loc := h.redirectOf(owner, "/setup/jellyfin", url.Values{"url": {"not a url"}}); loc != "/login?e=jf_url" {
		t.Fatalf("bad url: %s", loc)
	}
	if loc := h.redirectOf(owner, "/setup/jellyfin", url.Values{"url": {jf.URL}, "public": {"https://jf.example.com"}}); loc != "/login" {
		t.Fatalf("setup: %s", loc)
	}
	// A non-administrator cannot claim the install.
	f.qcUser = "friend"
	var start map[string]any
	h.post(owner, "/auth/jellyfin/qc/start", &start)
	if start["code"] != "123456" {
		t.Fatalf("start: %v", start)
	}
	var check map[string]any
	h.post(owner, "/auth/jellyfin/qc/check", &check)
	if check["pending"] != true {
		t.Fatalf("pending: %v", check)
	}
	f.approved = true
	h.post(owner, "/auth/jellyfin/qc/check", &check)
	if check["redirect"] != "/login?e=jf_not_admin" {
		t.Fatalf("non-admin first run: %v", check)
	}
	// The administrator does.
	f.qcUser, f.approved = "admin", false
	h.post(owner, "/auth/jellyfin/qc/start", &start)
	f.approved = true
	h.post(owner, "/auth/jellyfin/qc/check", &check)
	if check["redirect"] != "/admin" {
		t.Fatalf("admin first run: %v", check)
	}
	if !h.srv.isJellyfin(ctx) || f.keys != 1 {
		t.Fatalf("install: jellyfin %v, keys %d", h.srv.isJellyfin(ctx), f.keys)
	}
	for k, want := range map[string]string{keyMediaServer: "jellyfin", keyJFURL: jf.URL, keyJFPublic: "https://jf.example.com", keyServerID: f.serverID, keyServerName: "Home"} {
		if got, _ := h.srv.store.Setting(ctx, k); got != want {
			t.Errorf("setting %s: %q, want %q", k, got, want)
		}
	}
	sealed, _ := h.srv.store.Setting(ctx, keyJFKey)
	if key, err := h.srv.box.Open(sealed); key != "the-key" || err != nil {
		t.Fatalf("api key: %q %v", key, err)
	}
	u, err := h.srv.store.UserByAccount(ctx, "b53deadd-670c-44ad-87d3-7783c299e530")
	if err != nil || !u.IsOwner || !u.IsAdmin || u.Token != "" {
		t.Fatalf("owner: %+v %v", u, err)
	}
	if h.status(owner, "/admin/users") != http.StatusOK {
		t.Fatal("owner not signed in")
	}
	if f.logouts != 2 {
		t.Fatalf("logouts: %d", f.logouts) // the refused friend and the admin
	}
	// Plex sign-in is closed on a Jellyfin install.
	if h.status(owner, "/auth/plex/return") != http.StatusNotFound {
		t.Fatal("plex route open")
	}

	// Another account signs in with its password.
	friend := h.client()
	if loc := h.redirectOf(friend, "/auth/jellyfin/password", url.Values{"username": {"friend"}, "password": {"nope"}}); loc != "/login?e=jf_bad_login" {
		t.Fatalf("bad password: %s", loc)
	}
	if loc := h.redirectOf(friend, "/auth/jellyfin/password", url.Values{"username": {"friend"}, "password": {"pw-friend"}}); loc != "/" {
		t.Fatalf("password: %s", loc)
	}
	if fu, err := h.srv.store.UserByAccount(ctx, "0534d2b6-24ff-479c-b4e2-d05fded41061"); err != nil || fu.IsAdmin || fu.Token != "" {
		t.Fatalf("friend: %+v %v", fu, err)
	}
	// The hourly check signs out an account disabled on the server, never the owner.
	if h.status(friend, "/settings") != http.StatusOK {
		t.Fatal("friend not signed in")
	}
	f.mu.Lock()
	f.disabled["friend"] = true
	f.mu.Unlock()
	h.srv.checkAccess(ctx, time.Now().Add(25*time.Hour))
	if h.status(friend, "/settings") == http.StatusOK || h.status(owner, "/settings") != http.StatusOK {
		t.Fatal("access check: disabled friend still in, or owner out")
	}
	// A disabled account, or the same account on another server, is refused.
	if loc := h.redirectOf(h.client(), "/auth/jellyfin/password", url.Values{"username": {"friend"}, "password": {"pw-friend"}}); loc != "/login?e=no_access" {
		t.Fatalf("disabled: %s", loc)
	}
	f.disabled["friend"], f.serverID = false, "another"
	if loc := h.redirectOf(h.client(), "/auth/jellyfin/password", url.Values{"username": {"friend"}, "password": {"pw-friend"}}); loc != "/login?e=no_access" {
		t.Fatalf("other server: %s", loc)
	}
}

// Before setup only the chosen server can be signed in to; a Plex install
// keeps the Jellyfin routes closed.
func TestJellyfinRoutesClosedOnPlex(t *testing.T) {
	h := newHarness(t, config.Config{DataDir: t.TempDir()})
	c, _ := h.signIn("owner") // Plex first run
	if h.status(c, "/admin/users") != http.StatusOK {
		t.Fatal("plex owner not signed in")
	}
	if loc := h.redirectOf(h.client(), "/auth/jellyfin/password", url.Values{"username": {"a"}, "password": {"b"}}); loc != "" {
		t.Fatalf("jellyfin route open on plex: %s", loc)
	}
	if loc := h.redirectOf(h.client(), "/setup/jellyfin", url.Values{"url": {"http://x"}}); loc != "/login" {
		t.Fatalf("setup after setup: %s", loc)
	}
}

// Only the owner moves the server, and only to a server that has the same id
// and accepts the API key: a look-alike would otherwise receive the key.
func TestJellyfinAddressChange(t *testing.T) {
	h, f, jf := newJellyfinHarness(t)
	ctx := context.Background()
	owner := h.client()
	h.redirectOf(owner, "/setup/jellyfin", url.Values{"url": {jf.URL}})
	f.approved = true
	var start, check map[string]any
	h.post(owner, "/auth/jellyfin/qc/start", &start)
	h.post(owner, "/auth/jellyfin/qc/check", &check)
	if check["redirect"] != "/admin" {
		t.Fatalf("setup: %v", check)
	}
	// A look-alike: same public id, but it does not know the key.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/System/Info/Public" {
			fmt.Fprintf(w, `{"Id":%q,"ServerName":"Home"}`, f.serverID)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer fake.Close()
	h.redirectOf(owner, "/admin/jellyfin", url.Values{"url": {fake.URL}})
	if got, _ := h.srv.store.Setting(ctx, keyJFURL); got != jf.URL {
		t.Fatalf("look-alike accepted: %s", got)
	}
	// The same server under another address (here the same one, with a public URL).
	h.redirectOf(owner, "/admin/jellyfin", url.Values{"url": {jf.URL + "/"}, "public": {"https://jf.example.com"}})
	if got, _ := h.srv.store.Setting(ctx, keyJFPublic); got != "https://jf.example.com" {
		t.Fatalf("real server refused: %q", got)
	}
	// Another administrator may not.
	friend := h.client()
	h.redirectOf(friend, "/auth/jellyfin/password", url.Values{"username": {"friend"}, "password": {"pw-friend"}})
	fu, _ := h.srv.store.UserByAccount(ctx, "0534d2b6-24ff-479c-b4e2-d05fded41061")
	if err := h.srv.store.SetAdmin(ctx, fu.ID, true); err != nil {
		t.Fatal(err)
	}
	if res := h.form(friend, "/admin/jellyfin", url.Values{"url": {fake.URL}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("admin, not owner: %d", res.StatusCode)
	}
}

func (f *fakeJellyfin) count(n *int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *n
}

// Quick Connect polls are relayed within a budget, and a dead code stops
// being relayed (SEC-a2-01).
func TestJellyfinQCCheckIsRateLimited(t *testing.T) {
	h, f, jf := newJellyfinHarness(t)
	c := h.client()
	h.redirectOf(c, "/setup/jellyfin", url.Values{"url": {jf.URL}})
	var start, check map[string]any
	h.post(c, "/auth/jellyfin/qc/start", &start)
	for i := 0; i < 200; i++ {
		h.post(c, "/auth/jellyfin/qc/check", &check)
		if check["pending"] != true {
			t.Fatalf("poll %d: %v", i, check)
		}
	}
	if got := f.count(&f.connects); got > 15 {
		t.Fatalf("%d upstream polls for 200", got)
	}
	h.srv.checkOne = keyed{burst: 10, every: time.Second}
	f.mu.Lock()
	f.deadCode = true
	f.mu.Unlock()
	h.post(c, "/auth/jellyfin/qc/check", &check)
	if check["redirect"] != "/login?e=expired" {
		t.Fatalf("dead code: %v", check)
	}
	before := f.count(&f.connects)
	h.post(c, "/auth/jellyfin/qc/check", &check)
	if f.count(&f.connects) != before || check["redirect"] != "/login?e=expired" {
		t.Fatalf("dead code relayed again: %v", check)
	}
}

// Passwords have their own slow bucket, refusals are logged, and the input is
// bounded (SEC-a2-02).
func TestJellyfinPasswordLimits(t *testing.T) {
	h, f, jf := newJellyfinHarness(t)
	var logs bytes.Buffer
	h.srv.log = slog.New(slog.NewTextHandler(&logs, nil))
	c := h.client()
	h.redirectOf(c, "/setup/jellyfin", url.Values{"url": {jf.URL}})
	bad := url.Values{"username": {"friend"}, "password": {"nope"}}
	for i := 0; i < 5; i++ {
		if loc := h.redirectOf(c, "/auth/jellyfin/password", bad); loc != "/login?e=jf_bad_login" {
			t.Fatalf("try %d: %s", i, loc)
		}
	}
	if loc := h.redirectOf(c, "/auth/jellyfin/password", bad); loc != "/login?e=jf_wait" {
		t.Fatalf("6th try: %s", loc)
	}
	if got := f.count(&f.auths); got != 5 {
		t.Fatalf("%d upstream sign-ins, want 5", got)
	}
	// Guesses on one account do not lock the others out, whatever the case of
	// the name they are typed in.
	if loc := h.redirectOf(c, "/auth/jellyfin/password", url.Values{"username": {"FRIEND"}, "password": {"nope"}}); loc != "/login?e=jf_wait" {
		t.Fatalf("same account, other case: %s", loc)
	}
	if loc := h.redirectOf(c, "/auth/jellyfin/password", url.Values{"username": {"admin"}, "password": {"nope"}}); loc != "/login?e=jf_bad_login" {
		t.Fatalf("another account is locked out: %s", loc)
	}
	var start map[string]any
	h.post(c, "/auth/jellyfin/qc/start", &start)
	if start["code"] != "123456" {
		t.Fatalf("quick connect starved by passwords: %v", start)
	}
	if !strings.Contains(logs.String(), "bad Jellyfin password") || strings.Contains(logs.String(), "nope") {
		t.Fatalf("log: %s", logs.String())
	}
	// Oversized input is refused without reaching the server.
	big := url.Values{"username": {"admin"}, "password": {strings.Repeat("x", 1<<20)}}
	if loc := h.redirectOf(c, "/auth/jellyfin/password", big); loc != "/login?e=jf_bad_login" {
		t.Fatalf("oversized: %s", loc)
	}
	if got := f.count(&f.auths); got != 6 {
		t.Fatalf("oversized reached the server: %d", got)
	}
}

// A dead server answers at once with the real diagnosis (UX-a2-04).
func TestJellyfinQCStartUnreachable(t *testing.T) {
	h, _, jf := newJellyfinHarness(t)
	c := h.client()
	h.redirectOf(c, "/setup/jellyfin", url.Values{"url": {jf.URL}})
	jf.Close()
	var start map[string]any
	h.post(c, "/auth/jellyfin/qc/start", &start)
	if start["redirect"] != "/login?e=jf_unreachable" || start["disabled"] != nil {
		t.Fatalf("start: %v", start)
	}
}

// A failed first run leaves neither an API key on the server nor an account
// (SEC-a2-05, EDGE-a2-06).
func TestJellyfinFailedFirstRunLeavesNothing(t *testing.T) {
	h, f, jf := newJellyfinHarness(t)
	ctx := context.Background()
	c := h.client()
	h.redirectOf(c, "/setup/jellyfin", url.Values{"url": {jf.URL}})
	admin := url.Values{"username": {"admin"}, "password": {"pw-admin"}}

	f.mu.Lock()
	f.publicID = "another"
	f.mu.Unlock()
	if loc := h.redirectOf(c, "/auth/jellyfin/password", admin); loc != "/login?e=jf_unreachable" {
		t.Fatalf("other id: %s", loc)
	}
	if f.count(&f.keys) != 0 {
		t.Fatal("key created before the checks")
	}
	f.mu.Lock()
	f.publicID, f.keyFail = "", true
	f.mu.Unlock()
	if loc := h.redirectOf(c, "/auth/jellyfin/password", admin); loc != "/login?e=jf_unreachable" {
		t.Fatalf("key refused: %s", loc)
	}
	if users, _ := h.srv.store.Users(ctx); len(users) != 0 {
		t.Fatalf("stray accounts: %d", len(users))
	}
}

// isJellyfin never caches a value read before the install was claimed
// (EDGE-a2-07).
func TestJellyfinKindSurvivesFirstRunRace(t *testing.T) {
	h, f, jf := newJellyfinHarness(t)
	ctx := context.Background()
	c := h.client()
	h.redirectOf(c, "/setup/jellyfin", url.Values{"url": {jf.URL}})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.srv.isJellyfin(ctx)
				}
			}
		}()
	}
	if loc := h.redirectOf(c, "/auth/jellyfin/password", url.Values{"username": {"admin"}, "password": {"pw-admin"}}); loc != "/admin" {
		t.Fatalf("first run: %s", loc)
	}
	close(stop)
	wg.Wait()
	_ = f
	if !h.srv.isJellyfin(ctx) {
		t.Fatal("install forgot it is Jellyfin")
	}
}
