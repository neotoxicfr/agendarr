package server

import (
	"context"
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
