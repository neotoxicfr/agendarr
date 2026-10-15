package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// securityHeaders sets a strict baseline on every response. Scripts and styles
// only come from our own origin; images from our poster proxy and from
// plex.tv (account avatars, which redirect to secure.gravatar.com or
// *.plex.tv; CSP also applies to the redirect target).
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' https://plex.tv https://*.plex.tv https://secure.gravatar.com; connect-src 'self'; font-src 'self'; " +
		"manifest-src 'self'; worker-src 'self'; frame-ancestors 'none'; " +
		"base-uri 'self'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // a recover() value, compared as net/http does
					panic(v)
				}
				s.log.Error("panic", "path", r.URL.Path, "value", v)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// staticHandler serves embedded assets. URLs carry ?v=<version>, so they can be
// cached for a long time; a release changes the query string.
func (s *Server) staticHandler() http.Handler {
	files := http.FileServerFS(s.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) serveStaticFile(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(s.static, path.Clean(name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
}

// serveServiceWorker serves /sw.js from the root (a worker's scope cannot be
// wider than its path) with the release stamped in, so each release gets
// fresh caches and the old ones are purged on activation.
func (s *Server) serveServiceWorker(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.static, "js/sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b = bytes.ReplaceAll(b, []byte("__VERSION__"), []byte(Version))
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
