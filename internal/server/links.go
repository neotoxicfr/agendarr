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
	"github.com/neotoxicfr/agendarr/internal/jellyfin"
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

// resetLibrary drops the index age, so the next look rebuilds it at once:
// the media server was just bound, or moved.
func (s *Server) resetLibrary() {
	s.plexLib.mu.Lock()
	s.plexLib.at, s.plexLib.tried = time.Time{}, time.Time{}
	s.plexLib.mu.Unlock()
}

// WarmPlexLibrary starts indexing the Plex library at startup, so the first
// calendar after a restart already has its Plex links and tracks.
func (s *Server) WarmPlexLibrary(ctx context.Context) { s.plexKeys(ctx) }

func (s *Server) buildPlexKeys(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var keys map[string]string
	var err error
	if s.isJellyfin(ctx) {
		var jf *jellyfin.Client
		if jf, err = s.jellyfinClient(ctx); err == nil {
			keys, err = jf.Library(ctx)
		}
	} else {
		var srv *plex.Server
		if srv, err = s.plexServer(ctx); err == nil {
			keys, err = srv.Library(ctx)
		}
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
	// On Jellyfin a link needs the web client's address: the public one if
	// set, else the one agendarr reaches.
	webURL := plex.WebURL
	if s.isJellyfin(ctx) {
		base, _ := s.store.Setting(ctx, keyJFPublic)
		if base == "" {
			base, _ = s.store.Setting(ctx, keyJFURL)
		}
		webURL = func(server, item string) string { return jellyfin.WebURL(base, server, item) }
	}
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
					e.MediaURL = webURL(machine, rk)
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
		if v, err := s.itemTracks(r.Context(), rk, season, episode, kind == "series"); err == nil {
			versions = append(versions, v...) // never null, even for an absent item (EDGE-a1-03)
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
	cacheKey := func(ep int) string { return trackKey(showKey, season, ep) }
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
	if s.isJellyfin(ctx) {
		eps, err := s.jellyfinSeason(ctx, showKey, season)
		for _, ep := range todo {
			if v := eps[ep]; len(v) > 0 {
				out[strconv.Itoa(ep)] = v
			}
		}
		return out, err
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

// trackKey is the cache key of one episode's tracks.
func trackKey(showKey string, season, ep int) string {
	return fmt.Sprintf("%s/%d/%d", showKey, season, ep)
}

// itemTracks returns the tracks of one library item, cached ten minutes: a
// movie by its ratingKey, an episode (series) resolved on the Plex server.
func (s *Server) itemTracks(ctx context.Context, rk string, season, episode int, series bool) ([]plex.Version, error) {
	cacheKey := rk
	if series {
		cacheKey = trackKey(rk, season, episode)
	}
	if v, ok := s.cachedTracks(cacheKey); ok {
		return v, nil
	}
	v, err, _ := s.tracksSF.Do(cacheKey, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if s.isJellyfin(ctx) {
			if series {
				eps, err := s.jellyfinSeason(ctx, rk, season)
				return eps[episode], err
			}
			jf, err := s.jellyfinClient(ctx)
			if err != nil {
				return nil, err
			}
			return jf.Tracks(ctx, rk)
		}
		srv, err := s.plexServer(ctx)
		if err != nil {
			return nil, err
		}
		key := rk
		if series {
			if key, err = srv.EpisodeKey(ctx, rk, season, episode); err != nil {
				return nil, err
			}
			if key == "" { // not in Plex: an answer to cache, not an outage (EDGE-a1-03)
				return []plex.Version(nil), nil
			}
		}
		return srv.Tracks(ctx, key)
	})
	if err != nil {
		return nil, err
	}
	versions := v.([]plex.Version)
	s.storeTracks(cacheKey, versions)
	return versions, nil
}

// jellyfinSeason reads the tracks of a whole season in one call and caches
// each episode, as the Plex path does one by one.
func (s *Server) jellyfinSeason(ctx context.Context, seriesID string, season int) (map[int][]plex.Version, error) {
	jf, err := s.jellyfinClient(ctx)
	if err != nil {
		return nil, err
	}
	eps, err := jf.SeasonTracks(ctx, seriesID, season)
	if err != nil {
		return nil, err
	}
	out := map[int][]plex.Version{}
	for _, e := range eps {
		if e.Season == season {
			// A double file (S01E01-E02) is one item that covers every episode of its range (EDGE-a1-02).
			// The range is capped: the end comes from the server's metadata.
			for n := e.Episode; n <= min(max(e.Episode, e.EpisodeEnd), e.Episode+20); n++ {
				out[n] = e.Versions
				s.storeTracks(trackKey(seriesID, season, n), e.Versions)
			}
		}
	}
	return out, nil
}
