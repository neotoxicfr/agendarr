// Package server wires the HTTP routes, middlewares and templates.
package server

import (
	"bytes"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/neotoxicfr/agendarr/internal/config"
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
	return s, nil
}

// Handler returns the root handler with every middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("GET /static/", http.StripPrefix("/static/", s.staticHandler()))
	// The manifest and the service worker must be served from the root: a
	// worker's scope cannot be wider than the path it is served from.
	mux.HandleFunc("GET /manifest.webmanifest", s.serveStaticFile("manifest.webmanifest", "application/manifest+json"))
	mux.HandleFunc("GET /sw.js", s.serveStaticFile("js/sw.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /offline", s.handleOffline)
	mux.HandleFunc("GET /{$}", s.handleHome)
	return s.recoverer(securityHeaders(mux))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "home", pageData{Title: "agendarr"})
}

func (s *Server) handleOffline(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "offline", pageData{Title: "Hors ligne"})
}

// pageData is the common template payload.
type pageData struct {
	Title   string
	Version string
	Data    any
}

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

var funcs = template.FuncMap{}
