// Package server wires the HTTP routes, middlewares and templates.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/gob"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"
	"golang.org/x/sync/singleflight"

	"github.com/neotoxicfr/agendarr/internal/calendar"
	"github.com/neotoxicfr/agendarr/internal/config"
	"github.com/neotoxicfr/agendarr/internal/i18n"
	"github.com/neotoxicfr/agendarr/internal/personal"
	"github.com/neotoxicfr/agendarr/internal/plex"
	"github.com/neotoxicfr/agendarr/internal/secret"
	"github.com/neotoxicfr/agendarr/internal/store"
	"github.com/neotoxicfr/agendarr/web"
)

// Version is set at build time with -ldflags "-X .../server.Version=...".
var Version = "dev"

// Server holds the application dependencies.
type Server struct {
	cfg     config.Config
	store   *store.Store
	box     *secret.Box
	log     *slog.Logger
	tmpl    map[string]*template.Template
	static  fs.FS
	started time.Time

	i18n       *i18n.Catalog
	sessions   *scs.SessionManager
	plex       *plex.Client
	ownerKnown atomic.Bool // setup completed; never goes back to false

	pinLimit           limiter
	onInstancesChanged func()
	cal                *calendar.Service
	personal           *personal.Service
	plexConn           plexConn
	plexLib            plexLibrary
	posterSF           singleflight.Group
}

// New builds a Server and parses the templates.
func New(cfg config.Config, st *store.Store, box *secret.Box, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, store: st, box: box, log: log, started: time.Now()}
	var err error
	if s.static, err = fs.Sub(web.FS, "static"); err != nil {
		return nil, err
	}
	if s.tmpl, err = parseTemplates(); err != nil {
		return nil, err
	}
	if s.i18n, err = i18n.Load(web.FS, "locales"); err != nil {
		return nil, err
	}

	ctx := context.Background()
	clientID, err := st.Setting(ctx, keyPlexClientID)
	if err != nil {
		return nil, err
	}
	if clientID == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		clientID = "agendarr-" + hex.EncodeToString(b)
		if err := st.SetSetting(ctx, keyPlexClientID, clientID); err != nil {
			return nil, err
		}
	}
	s.plex = plex.New(clientID, Version)
	if cfg.PlexTVURL != "" {
		log.Warn("AGENDARR_PLEX_TV_URL set: plex.tv is replaced, test benches only", "url", cfg.PlexTVURL)
		s.plex.TVBase, s.plex.ClientsBase = cfg.PlexTVURL, cfg.PlexTVURL
	}

	s.cal = calendar.New(s.calendarSources, log)
	s.personal = s.newPersonal()
	s.onInstancesChanged = s.cal.Invalidate

	gob.Register([]string(nil)) // flash messages
	s.sessions = scs.New()
	s.sessions.Store = sqlite3store.New(st.DB)
	s.sessions.Lifetime = 400 * 24 * time.Hour
	s.sessions.IdleTimeout = 30 * 24 * time.Hour // sliding 30 days
	s.sessions.Cookie.Name = "agendarr_session"
	s.sessions.Cookie.HttpOnly = true
	s.sessions.Cookie.SameSite = http.SameSiteLaxMode
	s.sessions.Cookie.Secure = cfg.SecureCookies
	s.sessions.Cookie.Persist = true
	return s, nil
}

// Handler returns the root handler with every middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /offline", s.handleOffline)
	mux.HandleFunc("GET /privacy", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, http.StatusOK, "privacy", pageData{Title: "title.privacy"})
	})

	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("POST /auth/plex/start", s.handlePinStart)
	mux.HandleFunc("POST /auth/plex/check", s.handlePinCheck)
	mux.HandleFunc("GET /auth/plex/return", s.handlePinReturn)
	mux.HandleFunc("GET /auth/plex/done", s.handlePinDone)
	mux.HandleFunc("GET /setup/server", s.handleSetupServer)
	mux.HandleFunc("POST /setup/server", s.handleSetupServer)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("POST /lang", s.handleLang)

	mux.HandleFunc("GET /{$}", s.requireUser(s.handleHome))
	mux.HandleFunc("GET /api/calendar", s.requireUser(s.handleCalendarAPI))
	mux.HandleFunc("GET /img/{inst}/{id}", s.requireUser(s.handlePoster))
	mux.HandleFunc("GET /sync", s.requireUser(s.handleSync))
	mux.HandleFunc("POST /sync/new", s.requireUser(s.handleSyncNew))
	mux.HandleFunc("POST /sync/revoke", s.requireUser(s.handleSyncRevoke))
	mux.HandleFunc("GET /sync/qr.png", s.requireUser(s.handleSyncQR))
	mux.HandleFunc("GET /ics/{file}", s.handleFeed)
	mux.HandleFunc("GET /api/prefs", s.requireUser(s.handlePrefsGet))
	mux.HandleFunc("PUT /api/prefs", s.requireUser(s.handlePrefsPut))

	mux.HandleFunc("GET /admin", s.requireAdmin(s.handleAdmin))
	mux.HandleFunc("GET /admin/users", s.requireAdmin(s.handleAdminUsers))
	mux.HandleFunc("POST /admin/users/{id}/{action}", s.requireAdmin(s.handleAdminUserAction))
	mux.HandleFunc("GET /admin/instances", s.requireAdmin(s.handleAdminInstances))
	mux.HandleFunc("GET /admin/instances/new", s.requireAdmin(s.handleInstanceForm))
	mux.HandleFunc("POST /admin/instances/new", s.requireAdmin(s.handleInstanceSave))
	mux.HandleFunc("POST /admin/instances/test", s.requireAdmin(s.handleInstanceTest))
	mux.HandleFunc("GET /admin/instances/{id}", s.requireAdmin(s.handleInstanceForm))
	mux.HandleFunc("POST /admin/instances/{id}", s.requireAdmin(s.handleInstanceSave))
	mux.HandleFunc("POST /admin/instances/{id}/delete", s.requireAdmin(s.handleInstanceDelete))
	mux.HandleFunc("POST /admin/instances/{id}/import", s.requireAdmin(s.handleSeerrImport))

	// Cross-origin state-changing requests are rejected (Sec-Fetch-Site /
	// Origin checks), which is our CSRF defence alongside SameSite=Lax.
	csrf := http.NewCrossOriginProtection()
	app := s.sessions.LoadAndSave(s.loadUser(csrf.Handler(mux)))

	// Static assets, the service worker, the manifest and the health check
	// need no session: kept out of LoadAndSave, they cost no SQLite write
	// and carry no Set-Cookie (scs touches the session on every request).
	bare := http.NewServeMux()
	bare.Handle("GET /static/", http.StripPrefix("/static/", s.staticHandler()))
	bare.HandleFunc("GET /manifest.webmanifest", s.serveStaticFile("manifest.webmanifest", "application/manifest+json"))
	bare.HandleFunc("GET /sw.js", s.serveServiceWorker)
	bare.HandleFunc("GET /healthz", s.handleHealth)
	bare.Handle("/", app)
	return s.recoverer(securityHeaders(bare))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleOffline(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "offline", pageData{Title: "title.offline"})
}

// pageData is the common template payload.
type pageData struct {
	Title   string // i18n key; empty on the home page
	Version string
	User    *store.User
	Path    string
	Lang    string
	Langs   []string
	T       map[string]string
	JS      map[string]string // labels the front-end scripts need
	Flash   string
	Data    any
}

// jsPrefixes are the label groups exposed to app.js.
var jsPrefixes = []string{"login.", "js.", "cal."}

// render executes a page into a buffer first, so a template error becomes a
// clean 500 instead of a half-written page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, d pageData) {
	t, ok := s.tmpl[name]
	if !ok {
		s.log.Error("unknown template", "name", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d.Version = Version
	d.User = currentUser(r)
	d.Path = r.URL.Path
	d.Lang = s.i18n.Pick(r)
	d.Langs = s.i18n.Langs()
	d.T = s.i18n.Map(d.Lang)
	d.Flash = s.takeFlash(r, d.T)
	d.JS = map[string]string{}
	for k, v := range d.T {
		for _, p := range jsPrefixes {
			if strings.HasPrefix(k, p) {
				d.JS[k] = v
			}
		}
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", d); err != nil {
		s.log.Error("render", "name", name, "path", r.URL.Path, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func parseTemplates() (map[string]*template.Template, error) {
	pages, err := fs.Glob(web.FS, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	out := make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		t, err := template.New("").Funcs(funcs).ParseFS(web.FS, "templates/layout.html", "templates/partials/*.html", p)
		if err != nil {
			return nil, err
		}
		name := p[len("templates/pages/") : len(p)-len(".html")]
		out[name] = t
	}
	return out, nil
}

var funcs = template.FuncMap{
	"hasPrefix": strings.HasPrefix,
	"date": func(t map[string]string, v time.Time) string {
		return v.Local().Format(t["date.format"])
	},
}
