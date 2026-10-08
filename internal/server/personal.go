package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/neotoxicfr/agendarr/internal/calendar"
	"github.com/neotoxicfr/agendarr/internal/personal"
	"github.com/neotoxicfr/agendarr/internal/plex"
	"github.com/neotoxicfr/agendarr/internal/seerr"
	"github.com/neotoxicfr/agendarr/internal/store"
)

func (s *Server) newPersonal() *personal.Service {
	return personal.New(personal.Deps{
		Store:      s.store,
		Log:        s.log,
		Seerrs:     s.seerrClients,
		PlexServer: s.plexServer,
		PlexTV:     s.plex,
		UserToken:  func(u store.User) (string, error) { return s.box.Open(u.PlexToken) },
	})
}

func (s *Server) seerrClients(ctx context.Context) ([]*seerr.Client, error) {
	list, err := s.store.Instances(ctx)
	if err != nil {
		return nil, err
	}
	var out []*seerr.Client
	for _, i := range list {
		if i.Kind != store.KindSeerr || !i.Enabled {
			continue
		}
		key, err := s.box.Open(i.APIKey)
		if err != nil {
			continue
		}
		out = append(out, seerr.New(i.URL, key))
	}
	return out, nil
}

// plexConn remembers which advertised connection of the Plex server works.
type plexConn struct {
	mu     sync.Mutex
	uri    string
	picked time.Time
}

// plexServer returns a client for the bound Plex server with the owner's
// token. AGENDARR_PLEX_SERVER_URL forces the address (for example
// http://plex:32400 on a Docker network); otherwise the connections plex.tv
// advertises are tried, local ones first, and the winner is kept for an hour.
func (s *Server) plexServer(ctx context.Context) (*plex.Server, error) {
	sealed, err := s.store.Setting(ctx, keyServerToken)
	if err != nil {
		return nil, err
	}
	token, err := s.box.Open(sealed)
	if err != nil || token == "" {
		return nil, errors.New("no Plex server token: the owner must sign in once")
	}
	srv := &plex.Server{Token: token, ClientID: s.plex.ClientID}
	if s.cfg.PlexServerURL != "" {
		srv.BaseURL = s.cfg.PlexServerURL
		return srv, nil
	}
	s.plexConn.mu.Lock()
	defer s.plexConn.mu.Unlock()
	if s.plexConn.uri != "" && time.Since(s.plexConn.picked) < time.Hour {
		srv.BaseURL = s.plexConn.uri
		return srv, nil
	}
	raw, err := s.store.Setting(ctx, keyServerConnsJSON)
	if err != nil {
		return nil, err
	}
	var conns []plex.Connection
	_ = json.Unmarshal([]byte(raw), &conns)
	uri, err := plex.PickConnection(ctx, conns, token, s.plex.ClientID)
	if err != nil {
		return nil, err
	}
	s.plexConn.uri, s.plexConn.picked = uri, time.Now()
	srv.BaseURL = uri
	return srv, nil
}

// Prefs are the calendar settings a user saves; the iCal feed reads them too.
type Prefs struct {
	View    string      `json:"view,omitempty"`
	Mine    []string    `json:"mine"`
	Filters PrefFilters `json:"filters"`
	Keys    *bool       `json:"shortcuts,omitempty"` // single-key shortcuts on (default)
	// Sites are the external links shown on a detail sheet ("See on").
	Sites []string `json:"sites"`
}

// defaultSites are shown until a user picks their own.
var defaultSites = []string{"tmdb", "imdb"}

// cleanSites keeps the known sites, once each; nil (never chosen) gets the
// defaults, an empty list stays empty.
func cleanSites(in []string) []string {
	if in == nil {
		return slices.Clone(defaultSites)
	}
	out := []string{}
	for _, v := range in {
		if slices.Contains(calendar.Sites, v) && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// PrefFilters mirror the filter panel.
type PrefFilters struct {
	Movies          bool            `json:"movies"`
	Episodes        bool            `json:"episodes"`
	Status          string          `json:"status"`                   // all | available | upcoming
	HideDownloaded  bool            `json:"hideDownloaded,omitempty"` // before Status; read once, then dropped
	HideUnmonitored bool            `json:"hideUnmonitored"`
	Instances       map[string]bool `json:"instances"`
}

// normalize maps the old hideDownloaded checkbox onto Status and drops
// unknown values.
func (f *PrefFilters) normalize() {
	switch f.Status {
	case "all", "available", "upcoming":
	default:
		f.Status = "all"
		if f.HideDownloaded {
			f.Status = "upcoming"
		}
	}
	f.HideDownloaded = false
}

func defaultPrefs() Prefs {
	return Prefs{Mine: []string{}, Filters: PrefFilters{Movies: true, Episodes: true, Instances: map[string]bool{}}}
}

func (s *Server) loadPrefs(ctx context.Context, userID int64) Prefs {
	p := defaultPrefs()
	raw, err := s.store.Prefs(ctx, userID)
	if err == nil {
		_ = json.Unmarshal([]byte(raw), &p)
	}
	p.Mine = cleanSources(p.Mine)
	p.Filters.normalize()
	p.Sites = cleanSites(p.Sites)
	if p.Filters.Instances == nil {
		p.Filters.Instances = map[string]bool{}
	}
	return p
}

func cleanSources(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if personal.Valid(v) && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) handlePrefsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.loadPrefs(r.Context(), currentUser(r).ID))
}

func (s *Server) handlePrefsPut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	p := defaultPrefs()
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	p.Mine = cleanSources(p.Mine)
	p.Filters.normalize()
	p.Sites = cleanSites(p.Sites)
	if p.View != "" && p.View != "agenda" && p.View != "week" && p.View != "month" {
		p.View = ""
	}
	if len(p.Filters.Instances) > 50 {
		http.Error(w, "too many instances", http.StatusBadRequest)
		return
	}
	clean, _ := json.Marshal(p)
	if err := s.store.SetPrefs(r.Context(), currentUser(r).ID, string(clean)); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
