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

// WarmPlexLibrary starts indexing the Plex library at startup, so the first
// calendar after a restart already has its Plex links and tracks.
func (s *Server) WarmPlexLibrary(ctx context.Context) { s.plexKeys(ctx) }

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
// for an episode): the versions and tracks Plex has for it, or an empty list
// when Plex does not have it. With &season=&episodes=1,2,3 it answers a whole
// season bundle at once: {"episodes": {"1": [versions], ...}}.
func (s *Server) handleTracks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind, key, _ := strings.Cut(q.Get("group"), ":")
	season, _ := strconv.Atoi(q.Get("season"))
	rk, ok := s.plexKeys(r.Context())[key]
	known := ok && (kind == "movie" || kind == "series")

	if q.Has("episodes") {
		out := map[string][]plex.Version{}
		var err error
		if known && kind == "series" {
			out, err = s.seasonTracks(r.Context(), rk, season, q.Get("episodes"))
		}
		resp := map[string]any{"episodes": out}
		if err != nil {
			s.log.Info("plex season tracks", "group", q.Get("group"), "err", err)
			resp["unreachable"] = true // Plex did not answer: not "not in Plex" (UX-07)
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	versions := []plex.Version{}
	if known {
		episode, _ := strconv.Atoi(q.Get("episode"))
		cacheKey, item := rk, func(*plex.Server, context.Context) (string, error) { return rk, nil }
		if kind == "series" {
			cacheKey = fmt.Sprintf("%s/%d/%d", rk, season, episode)
			item = func(srv *plex.Server, ctx context.Context) (string, error) {
				return srv.EpisodeKey(ctx, rk, season, episode)
			}
		}
		if v, err := s.itemTracks(r.Context(), cacheKey, item); err == nil {
			versions = v
		} else {
			s.log.Info("plex tracks", "group", q.Get("group"), "err", err)
			writeJSON(w, http.StatusOK, map[string]any{"versions": versions, "unreachable": true})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

// seasonTracks reads the tracks of several episodes of one season: the Plex
// server and the show's episode list once, then four episodes at a time,
// within the request and 30 s at most; the first failure stops the rest, so
// a slow Plex costs one timeout, not one per episode (SEC-03).
func (s *Server) seasonTracks(ctx context.Context, showKey string, season int, list string) (map[string][]plex.Version, error) {
	out := map[string][]plex.Version{}
	var episodes []int
	for _, v := range strings.Split(list, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 && len(episodes) < 60 {
			episodes = append(episodes, n)
		}
	}
	if len(episodes) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cacheKey := func(ep int) string { return fmt.Sprintf("%s/%d/%d", showKey, season, ep) }
	var todo []int
	for _, ep := range episodes {
		if v, ok := s.cachedTracks(cacheKey(ep)); ok {
			if len(v) > 0 {
				out[strconv.Itoa(ep)] = v
			}
		} else {
			todo = append(todo, ep)
		}
	}
	if len(todo) == 0 {
		return out, nil
	}
	srv, err := s.plexServer(ctx)
	if err != nil {
		return out, err
	}
	keys, err := srv.EpisodeKeys(ctx, showKey)
	if err != nil {
		return out, err
	}
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, ep := range todo {
		rk, ok := keys[[2]int{season, ep}]
		if !ok {
			continue // not in Plex
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			v, err := srv.Tracks(ctx, rk)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				return
			}
			s.storeTracks(cacheKey(ep), v)
			if len(v) > 0 {
				out[strconv.Itoa(ep)] = v
			}
		}()
	}
	wg.Wait()
	return out, firstErr
}

func (s *Server) cachedTracks(key string) ([]plex.Version, bool) {
	tc := &s.tracks
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if e, hit := tc.m[key]; hit && time.Since(e.at) < tracksTTL {
		return e.versions, true
	}
	return nil, false
}

func (s *Server) storeTracks(key string, v []plex.Version) {
	tc := &s.tracks
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.m == nil || len(tc.m) > 500 {
		tc.m = map[string]trackEntry{}
	}
	tc.m[key] = trackEntry{at: time.Now(), versions: v}
}

// itemTracks returns the tracks of one library item, cached ten minutes;
// item resolves its ratingKey on the Plex server.
func (s *Server) itemTracks(ctx context.Context, cacheKey string, item func(*plex.Server, context.Context) (string, error)) ([]plex.Version, error) {
	if v, ok := s.cachedTracks(cacheKey); ok {
		return v, nil
	}
	v, err, _ := s.tracksSF.Do(cacheKey, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		srv, err := s.plexServer(ctx)
		if err != nil {
			return nil, err
		}
		rk, err := item(srv, ctx)
		if err != nil {
			return nil, err
		}
		return srv.Tracks(ctx, rk)
	})
	if err != nil {
		return nil, err
	}
	versions := v.([]plex.Version)
	s.storeTracks(cacheKey, versions)
	return versions, nil
}
