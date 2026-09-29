package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neotoxicfr/agendarr/internal/config"
	"github.com/neotoxicfr/agendarr/internal/secret"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// fakePlex serves plex.tv for a set of accounts keyed by token.
type fakePlex struct {
	mu       sync.Mutex
	nextPin  string // token the next PIN resolves to
	accounts map[string]string // token -> user JSON
	servers  map[string]string // token -> resources JSON
}

func (f *fakePlex) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/pins", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":1,"code":"c"}`)
	})
	mux.HandleFunc("GET /api/v2/pins/1", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "code": "c", "authToken": f.nextPin})
	})
	mux.HandleFunc("GET /api/v2/user", func(w http.ResponseWriter, r *http.Request) {
		u, ok := f.accounts[r.Header.Get("X-Plex-Token")]
		if !ok {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, u)
	})
	mux.HandleFunc("GET /api/v2/resources", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, f.servers[r.Header.Get("X-Plex-Token")])
	})
	return mux
}

const (
	srvA = `{"name":"NAS","product":"Plex Media Server","clientIdentifier":"A","provides":"server","owned":%s,"accessToken":"atA","connections":[]}`
	srvB = `{"name":"Seedbox","product":"Plex Media Server","clientIdentifier":"B","provides":"server","owned":true,"accessToken":"atB","connections":[]}`
)

func owned(s string, own bool) string {
	if own {
		return strings.Replace(s, "%s", "true", 1)
	}
	return strings.Replace(s, "%s", "false", 1)
}

type harness struct {
	t    *testing.T
	fp   *fakePlex
	srv  *Server
	http *httptest.Server
}

func newHarness(t *testing.T, cfg config.Config) *harness {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secret.New(make([]byte, 32))
	s, err := New(cfg, st, box, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	fp := &fakePlex{
		accounts: map[string]string{
			"owner":    `{"id":1,"username":"neo","email":"neo@x"}`,
			"friend":   `{"id":2,"username":"friend","email":"f@x"}`,
			"stranger": `{"id":3,"username":"stranger"}`,
			"multi":    `{"id":4,"username":"multi"}`,
		},
		servers: map[string]string{
			"owner":    "[" + owned(srvA, true) + "]",
			"friend":   "[" + owned(srvA, false) + "]",
			"stranger": "[]",
			"multi":    "[" + owned(srvA, true) + "," + srvB + "]",
		},
	}
	pt := httptest.NewServer(fp.handler())
	t.Cleanup(pt.Close)
	s.plex.TVBase, s.plex.ClientsBase = pt.URL, pt.URL
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return &harness{t: t, fp: fp, srv: s, http: hs}
}

// signIn runs the popup flow for token and returns where the app sends the
// browser, with the client carrying the session cookie.
func (h *harness) signIn(token string) (*http.Client, string) {
	h.t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	h.fp.mu.Lock()
	h.fp.nextPin = token
	h.fp.mu.Unlock()
	var start map[string]string
	h.post(c, "/auth/plex/start", &start)
	if !strings.Contains(start["popupUrl"], "code=c") {
		h.t.Fatalf("start: %v", start)
	}
	var check map[string]any
	h.post(c, "/auth/plex/check", &check)
	next, _ := check["redirect"].(string)
	return c, next
}

func (h *harness) post(c *http.Client, path string, out any) {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.http.URL+path, nil)
	req.Header.Set("Origin", h.http.URL)
	res, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			h.t.Fatalf("%s: %v", path, err)
		}
	}
}

func (h *harness) status(c *http.Client, path string) int {
	h.t.Helper()
	res, err := c.Get(h.http.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestSetupAndAccess(t *testing.T) {
	h := newHarness(t, config.Config{})

	// Nothing configured: the calendar sends to the sign-in page.
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if code := h.status(anon, "/"); code != http.StatusSeeOther {
		t.Fatalf("anonymous home: %d", code)
	}
	// A shared (non-owner) account cannot complete the setup.
	if _, next := h.signIn("friend"); next != "/login?e=no_owned_server" {
		t.Fatalf("friend during setup: %q", next)
	}
	// The owner becomes admin and lands on /admin.
	owner, next := h.signIn("owner")
	if next != "/admin" {
		t.Fatalf("owner: %q", next)
	}
	if code := h.status(owner, "/admin/users"); code != 200 {
		t.Fatalf("owner admin: %d", code)
	}
	// Someone with access to server A is a plain user.
	friend, next := h.signIn("friend")
	if next != "/" {
		t.Fatalf("friend: %q", next)
	}
	if code := h.status(friend, "/"); code != 200 {
		t.Fatalf("friend home: %d", code)
	}
	if code := h.status(friend, "/admin/users"); code != http.StatusForbidden {
		t.Fatalf("friend admin: %d", code)
	}
	// No access to server A: refused.
	if _, next := h.signIn("stranger"); next != "/login?e=no_access" {
		t.Fatalf("stranger: %q", next)
	}
	// A cross-origin POST is rejected (CSRF).
	req, _ := http.NewRequest("POST", h.http.URL+"/admin/users/2/promote", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := owner.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST: %d", res.StatusCode)
	}
}

func TestSetupSeveralServers(t *testing.T) {
	h := newHarness(t, config.Config{})
	c, next := h.signIn("multi")
	if next != "/setup/server" {
		t.Fatalf("multi: %q", next)
	}
	req, _ := http.NewRequest("POST", h.http.URL+"/setup/server", strings.NewReader("server=B"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.http.URL)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Location") != "/admin" {
		t.Fatalf("choose server: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// Server B is bound: an account shared only on A is refused.
	if _, next := h.signIn("friend"); next != "/login?e=no_access" {
		t.Fatalf("friend on B: %q", next)
	}
}

func TestSetupRestrictedUser(t *testing.T) {
	h := newHarness(t, config.Config{SetupPlexUser: "someone@else"})
	if _, next := h.signIn("owner"); next != "/login?e=setup_denied" {
		t.Fatalf("restricted setup: %q", next)
	}
	h2 := newHarness(t, config.Config{SetupPlexUser: "NEO@x"})
	if _, next := h2.signIn("owner"); next != "/admin" {
		t.Fatalf("allowed setup: %q", next)
	}
}

func TestAccessCheckRevokes(t *testing.T) {
	h := newHarness(t, config.Config{})
	h.signIn("owner")
	friend, _ := h.signIn("friend")
	// The owner stops sharing: the next check signs the friend out.
	h.fp.servers["friend"] = "[]"
	h.srv.checkAccess(t.Context(), timeNowPlusDays(2))
	if code := h.status(friend, "/"); code != http.StatusSeeOther {
		t.Fatalf("revoked friend: %d", code)
	}
}

func timeNowPlusDays(d int) time.Time { return time.Now().Add(time.Duration(d) * 24 * time.Hour) }
