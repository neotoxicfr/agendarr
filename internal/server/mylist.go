package server

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neotoxicfr/agendarr/internal/arr"
	"github.com/neotoxicfr/agendarr/internal/calendar"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// "My list" shows what a person will be notified about: the movies and series
// of the Radarr and Sonarr libraries that match their Seerr requests, their
// Plex shows in progress or watchlist (the notification sources they picked),
// or that they asked to be notified about by hand. Seerr and Plex only give
// ids, so the libraries supply titles, posters and dates.

// catalogTTL is how long the libraries are served before a background refresh.
const catalogTTL = 30 * time.Minute

// catalogItem is one movie or series of an instance's library.
type catalogItem struct {
	Group     string
	Kind      string // movie | series
	Title     string
	Year      int
	Poster    string
	Keys      []string
	Instance  string
	HasFile   bool       // movie downloaded, or series with at least one episode file
	Monitored bool       //
	Next      *time.Time // next release of a movie, next episode of a series
	Status    string     // series: continuing | ended | upcoming
}

type catalog struct {
	mu      sync.Mutex
	items   []catalogItem
	at      time.Time
	tried   time.Time
	running bool
}

// catalogItems returns the libraries, rebuilding them behind when stale. The
// first call after a start waits for the build (bounded by the instances'
// own timeouts), later ones never do.
func (s *Server) catalogItems(ctx context.Context) []catalogItem {
	c := &s.catalog
	c.mu.Lock()
	items, at, running := c.items, c.at, c.running
	stale := time.Since(at) > catalogTTL && time.Since(c.tried) > plexLibraryBackoff
	if stale && !running {
		c.running, c.tried = true, time.Now()
	}
	c.mu.Unlock()
	if !stale || running {
		return items
	}
	if items == nil {
		s.buildCatalog(ctx)
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.items
	}
	go s.buildCatalog(context.WithoutCancel(ctx))
	return items
}

func (s *Server) buildCatalog(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	srcs, err := s.calendarSources(ctx)
	var out []catalogItem
	now := time.Now()
	for _, src := range srcs {
		c := arr.New(src.URL, src.APIKey)
		switch src.Kind {
		case store.KindRadarr:
			movies, ferr := c.AllMovies(ctx)
			if ferr != nil {
				s.log.Warn("radarr library", "instance", src.Name, "err", ferr)
				err = ferr
				continue
			}
			for _, m := range movies {
				k := calendar.Keys(m.TmdbID, 0, m.ImdbID, false)
				it := catalogItem{Group: calendar.MovieGroup(src.ID, m.ID, k), Kind: "movie", Title: m.Title, Year: m.Year,
					Poster: calendar.PosterURL(src.ID, m.ID, m.Images), Keys: k, Instance: src.Name, HasFile: m.HasFile, Monitored: m.Monitored}
				for _, d := range []*time.Time{m.InCinemas, m.DigitalRelease, m.PhysicalRelease} {
					if d != nil && d.After(now.Add(-24*time.Hour)) && (it.Next == nil || d.Before(*it.Next)) {
						it.Next = d
					}
				}
				out = append(out, it)
			}
		case store.KindSonarr:
			series, ferr := c.AllSeries(ctx)
			if ferr != nil {
				s.log.Warn("sonarr library", "instance", src.Name, "err", ferr)
				err = ferr
				continue
			}
			for _, sr := range series {
				k := calendar.Keys(sr.TmdbID, sr.TvdbID, sr.ImdbID, true)
				out = append(out, catalogItem{Group: calendar.SeriesGroup(src.ID, sr.ID, k), Kind: "series", Title: sr.Title,
					Year: sr.Year, Poster: calendar.PosterURL(src.ID, sr.ID, sr.Images), Keys: k, Instance: src.Name,
					HasFile: sr.Statistics.EpisodeFileCount > 0, Monitored: sr.Monitored, Next: sr.NextAiring, Status: sr.Status})
			}
		}
	}
	c := &s.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = false
	if err != nil && len(out) == 0 && c.items != nil {
		return // keep the last good libraries
	}
	c.items, c.at = mergeCatalog(out), time.Now()
}

// mergeCatalog folds the same movie or series seen by several instances.
func mergeCatalog(items []catalogItem) []catalogItem {
	byGroup := map[string]int{}
	var out []catalogItem
	for _, it := range items {
		if i, ok := byGroup[it.Group]; ok {
			m := &out[i]
			m.HasFile = m.HasFile || it.HasFile
			m.Monitored = m.Monitored || it.Monitored
			if it.Next != nil && (m.Next == nil || it.Next.Before(*m.Next)) {
				m.Next = it.Next
			}
			if !strings.Contains(m.Instance, it.Instance) {
				m.Instance += ", " + it.Instance
			}
			continue
		}
		byGroup[it.Group] = len(out)
		out = append(out, it)
	}
	return out
}

// listItem is one entry of "my list", as sent to the browser.
type listItem struct {
	Group     string     `json:"group"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Year      int        `json:"year,omitempty"`
	Poster    string     `json:"poster,omitempty"`
	Sources   []string   `json:"sources"` // seerr | watching | watchlist | manual
	HasFile   bool       `json:"hasFile"`
	Monitored bool       `json:"monitored"`
	Next      *time.Time `json:"next,omitempty"`
	Status    string     `json:"status,omitempty"`
	Added     *time.Time `json:"added,omitempty"` // manual: when
	InLibrary bool       `json:"inLibrary"`
}

// handleMyListAPI serves the list: library items matching the person's
// notification sources, and what they follow by hand.
func (s *Server) handleMyListAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := currentUser(r)
	sources := cleanNotify(s.loadPrefs(ctx, u.ID).Notify)
	follows, err := s.store.FollowList(ctx, u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// One look per source, without waiting on a sync, so each item can say
	// where it comes from.
	keys := map[string]map[string]bool{}
	errs := map[string]string{}
	for _, src := range sources {
		res := s.personal.Peek(ctx, *u, []string{src})
		keys[src] = res.Keys
		for k, v := range res.Errors {
			errs[k] = v
		}
	}
	out := []listItem{}
	seen := map[string]bool{}
	for _, it := range s.catalogItems(ctx) {
		var from []string
		for _, src := range sources {
			if anyKey(it.Keys, keys[src]) {
				from = append(from, src)
			}
		}
		f, manual := follows[it.Group]
		if manual {
			from = append(from, "manual")
		}
		if len(from) == 0 {
			continue
		}
		seen[it.Group] = true
		li := listItem{Group: it.Group, Kind: it.Kind, Title: it.Title, Year: it.Year, Poster: it.Poster, Sources: from,
			HasFile: it.HasFile, Monitored: it.Monitored, Next: it.Next, Status: it.Status, InLibrary: true}
		if manual {
			li.Added = &f.Created
		}
		out = append(out, li)
	}
	// Followed by hand but gone from the libraries: still listed, to remove.
	for g, f := range follows {
		if seen[g] {
			continue
		}
		kind, _, _ := strings.Cut(g, ":")
		out = append(out, listItem{Group: g, Kind: kind, Title: f.Title, Sources: []string{"manual"}, Added: &f.Created})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "sources": sources, "errors": errs})
}

// handleMyList renders the page; the list itself comes from /api/mylist.
func (s *Server) handleMyList(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "mylist", pageData{Title: "title.mylist"})
}
