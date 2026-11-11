package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neotoxicfr/agendarr/internal/config"
	"github.com/neotoxicfr/agendarr/internal/store"
)

func renderPage(h *harness, name string, data any) string {
	rec := httptest.NewRecorder()
	// The session middleware must be around render, as it is in the app.
	h.srv.sessions.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.srv.render(w, r, 200, name, pageData{Data: data})
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Body.String()
}

// The password form stays open after a refused or throttled attempt (UX-a5-02).
func TestLoginKeepsPasswordFormOpenOnError(t *testing.T) {
	h := newHarness(t, config.Config{})
	page := func(e string) string {
		return renderPage(h, "login", struct {
			Setup, Jellyfin bool
			Error, Server   string
			JFChosen        string
		}{Jellyfin: true, Error: e})
	}
	for _, e := range []string{"err.jf_bad_login", "err.jf_wait"} {
		if !strings.Contains(page(e), "data-jf-password open") {
			t.Errorf("%s: password form not open", e)
		}
	}
	if strings.Contains(page(""), "data-jf-password open") {
		t.Error("password form open without an error")
	}
}

// The initial is a rune, not a byte (EDGE-a5-04).
func TestAdminUsersInitialIsARune(t *testing.T) {
	h := newHarness(t, config.Config{})
	out := renderPage(h, "admin_users", map[string]any{"Users": []store.User{{Username: "Élodie", IsOwner: true}}, "Server": ""})
	if !strings.Contains(out, ">É<") {
		t.Errorf("initial not found in %q", out)
	}
}
