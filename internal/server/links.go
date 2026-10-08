package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neotoxicfr/agendarr/internal/calendar"
	"github.com/neotoxicfr/agendarr/internal/plex"
)

// plexLibraryTTL is how long the server's library index is served before a
// refresh; plexLibraryBackoff how long Plex is left alone after a failure.
const (
	plexLibraryTTL     = 30 * time.Minute
	plexLibraryBackoff = time.Minute
)

// plexLibrary caches the external id -> ratingKey index of the Plex server.
type plexLibrary struct {
	mu      sync.Mutex
	keys    map[string]string
	at      time.Time // last success
	tried   time.Time // last attempt
	running bool
}

// plexKeys returns the library index, or nil until the first build is done.
// It never waits on Plex: a missing or stale index is rebuilt in the
// background, so the calendar is not slowed down by a large library.
func (s *Server) plexKeys(ctx context.Context) map[string]string {
	l := &s.plexLib
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if !l.running && now.Sub(l.at) > plexLibraryTTL && now.Sub(l.tried) > plexLibraryBackoff {
		l.running, l.tried = true, now
		go s.buildPlexKeys(context.WithoutCancel(ctx))
	}
	return l.keys
}

func (s *Server) buildPlexKeys(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var keys map[string]string
	srv, err := s.plexServer(ctx)
	if err == nil {
		keys, err = srv.Library(ctx)
	}
	l := &s.plexLib
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running = false
	if err != nil {
		s.log.Warn("plex library", "err", err)
		return
	}
	l.keys, l.at = keys, time.Now()
}

// addLinks sets the Plex link of each event, and for admins the links to
// the Radarr and Sonarr instances that have a public address.
func (s *Server) addLinks(ctx context.Context, events []calendar.Event, admin bool) {
	lib := s.plexKeys(ctx)
	machine, _ := s.store.Setting(ctx, keyServerID)
	public := map[int64]string{}
	if admin {
		if list, err := s.store.Instances(ctx); err == nil {
			for _, i := range list {
				if i.PublicURL != "" {
					public[i.ID] = i.PublicURL
				}
			}
		}
	}
	for i := range events {
		e := &events[i]
		if machine != "" {
			for _, k := range e.Keys {
				if rk, ok := lib[k]; ok {
					e.PlexURL = plex.WebURL(machine, rk)
					break
				}
			}
		}
		for _, a := range e.Arr {
			if base, ok := public[a.InstanceID]; ok {
				e.ArrLinks = append(e.ArrLinks, calendar.Link{Label: a.Instance, URL: base + a.Path})
			}
		}
	}
}

// trackCache keeps the tracks of recently opened items for a while.
type trackCache struct {
	mu sync.Mutex
	m  map[string]trackEntry
}

type trackEntry struct {
	at       time.Time
	versions []plex.Version
}

const tracksTTL = 10 * time.Minute

// handleTracks serves /api/tracks?group=movie:tmdb:1 (and &season=&episode=
// for an episode): the audio and subtitle tracks Plex has for it, or an
// empty list when Plex does not have it.
func (s *Server) handleTracks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind, key, _ := strings.Cut(q.Get("group"), ":")
	season, _ := strconv.Atoi(q.Get("season"))
	episode, _ := strconv.Atoi(q.Get("episode"))
	empty := map[string]any{"versions": []plex.Version{}}
	rk, ok := s.plexKeys(r.Context())[key]
	if !ok || (kind != "movie" && kind != "series") {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	cacheKey := fmt.Sprintf("%s/%d/%d", rk, season, episode)
	if kind == "movie" {
		cacheKey = rk
	}
	tc := &s.tracks
	tc.mu.Lock()
	if e, hit := tc.m[cacheKey]; hit && time.Since(e.at) < tracksTTL {
		tc.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"versions": e.versions})
		return
	}
	tc.mu.Unlock()

	v, err, _ := s.tracksSF.Do(cacheKey, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
		defer cancel()
		srv, err := s.plexServer(ctx)
		if err != nil {
			return nil, err
		}
		item := rk
		if kind == "series" {
			if item, err = srv.EpisodeKey(ctx, rk, season, episode); err != nil {
				return nil, err
			}
		}
		return srv.Tracks(ctx, item)
	})
	if err != nil {
		s.log.Info("plex tracks", "group", q.Get("group"), "err", err)
		writeJSON(w, http.StatusOK, empty)
		return
	}
	versions := v.([]plex.Version)
	tc.mu.Lock()
	if tc.m == nil || len(tc.m) > 500 {
		tc.m = map[string]trackEntry{}
	}
	tc.m[cacheKey] = trackEntry{at: time.Now(), versions: versions}
	tc.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}
