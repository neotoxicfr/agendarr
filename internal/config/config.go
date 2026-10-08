// Package config loads and validates the runtime configuration from the
// environment. A bad value fails loudly at startup, never silently.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Config is the validated runtime configuration.
type Config struct {
	// Listen is the HTTP listen address, e.g. ":8080".
	Listen string
	// DataDir holds the SQLite database, the encryption key and caches.
	DataDir string
	// BaseURL is the public URL of the app, used for Plex forwardUrl and iCal
	// links. Optional: when empty it is derived from each request.
	BaseURL string
	// PlexServerURL forces the address of the Plex Media Server (for example
	// http://plex:32400 on a shared Docker network). Optional: by default the
	// connections advertised by plex.tv are tried, local ones first.
	PlexServerURL string
	// PlexTVURL replaces https://plex.tv and https://clients.plex.tv. Only
	// for test benches running a fake plex.tv; never set it in production.
	PlexTVURL string
	// SecureCookies sets the Secure flag on cookies: on, unless BaseURL is
	// plain http.
	SecureCookies bool
}

// Load reads the AGENDARR_* environment variables.
func Load() (Config, error) {
	c := Config{
		Listen:  env("AGENDARR_LISTEN", ":8080"),
		DataDir: env("AGENDARR_DATA_DIR", "./data"),
		BaseURL: strings.TrimRight(os.Getenv("AGENDARR_BASE_URL"), "/"),
		// Secure unless the public URL is explicitly plain http.
		SecureCookies: true,
		PlexTVURL:     strings.TrimRight(os.Getenv("AGENDARR_PLEX_TV_URL"), "/"),
		PlexServerURL: strings.TrimRight(os.Getenv("AGENDARR_PLEX_SERVER_URL"), "/"),
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return c, fmt.Errorf("AGENDARR_BASE_URL: want an absolute http(s) URL, got %q", c.BaseURL)
		}
		c.SecureCookies = u.Scheme == "https"
	}
	if c.PlexServerURL != "" {
		if u, err := url.Parse(c.PlexServerURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return c, fmt.Errorf("AGENDARR_PLEX_SERVER_URL: want an absolute http(s) URL, got %q", c.PlexServerURL)
		}
	}
	switch v := strings.ToLower(os.Getenv("AGENDARR_SECURE_COOKIES")); v {
	case "":
	case "true", "1":
		c.SecureCookies = true
	case "false", "0":
		c.SecureCookies = false
	default:
		return c, fmt.Errorf("AGENDARR_SECURE_COOKIES: want true or false, got %q", v)
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return c, fmt.Errorf("AGENDARR_DATA_DIR: %w", err)
	}
	return c, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
