// Package personal builds each user's personal filters: what they requested
// in Seerr, and what they watch on Plex (shows in progress, watchlist,
// everything watched), as sets of external ids ("tmdb:1", "tvdb:2").
package personal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/neotoxicfr/agendarr/internal/plex"
	"github.com/neotoxicfr/agendarr/internal/seerr"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// Sources of the personal filters.
const (
	SourceSeerr     = "seerr"     // my Seerr requests
	SourceWatching  = "watching"  // shows watched in the last 90 days
	SourceWatchlist = "watchlist" // Plex watchlist
	SourceWatched   = "watched"   // everything ever watched
)

// All lists the sources in display order.
var All = []string{SourceSeerr, SourceWatching, SourceWatchlist, SourceWatched}

// Valid reports whether s is a known source.
func Valid(s string) bool {
	for _, v := range All {
		if v == s {
			return true
		}
	}
	return false
}

// How long a synced source is served before refreshing. "watched" walks the
// whole history, so it refreshes less often.
var ttl = map[string]time.Duration{
	SourceSeerr:     10 * time.Minute,
	SourceWatching:  30 * time.Minute,
	SourceWatchlist: 30 * time.Minute,
	SourceWatched:   6 * time.Hour,
}

// watchingWindow is how far back "shows in progress" looks.
const watchingWindow = 90 * 24 * time.Hour

// Errors shown to the user.
var (
	ErrNoSeerr       = errors.New("no Seerr instance")
	ErrNoSeerrUser   = errors.New("no Seerr account linked to this Plex account")
	ErrNoPlexServer  = errors.New("plex server unreachable")
	ErrPlexSignedOut = errors.New("plex token expired, sign in again")
	ErrSyncing       = errors.New("first sync still running")
)

// Deps are provided by the server.
type Deps struct {
	Store *store.Store
	Log   *slog.Logger
	// Seerrs returns the enabled Seerr instances.
	Seerrs func(ctx context.Context) ([]*seerr.Client, error)
	// PlexServer returns a client for the bound Plex server (owner token).
	PlexServer func(ctx context.Context) (*plex.Server, error)
	// PlexTV and UserToken serve the watchlist.
	PlexTV    *plex.Client
	UserToken func(u store.User) (string, error)
}

// Service computes and caches the personal sets.
type Service struct {
	d   Deps
	now func() time.Time
	sf  singleflight.Group

	mu         sync.Mutex
	seerrIdx   *seerrIndex
	seerrTime  time.Time
	seerrErr   error
	seerrErrAt time.Time

	// media mirrors user_media / user_media_sync per "userID/source", so a
	// request does not reread thousands of rows from the single SQLite
	// connection. Only the sync writes it; entries past their TTL are swept.
	media    map[string]mediaEntry
	mediaGen uint64 // bumped by each sync write, so a slower store read never overwrites it
}

// mediaEntry is one synced source as stored. createdAt tells a deleted
// user's entry from a new user given the same id.
type mediaEntry struct {
	createdAt time.Time
	set       map[string]bool
	at        time.Time
	syncErr   string
}

// New returns a Service.
func New(d Deps) *Service { return &Service{d: d, now: time.Now, media: map[string]mediaEntry{}} }

// seerrIndex maps media keys to their requesters, across Seerr instances.
type seerrIndex struct {
	byKey     map[string][]string        // media key -> requester display names
	byAccount map[string]map[string]bool // media-server account (seerr.AccountKey) -> media keys requested
	byName    map[string]map[string]bool // lower-case username/email -> keys (older users without an account id)
	accounts  map[string]bool            // media-server accounts of the Seerr users
}

// RequestIndex returns a lookup from media key to requesters, for annotating
// many events at once. It is nil when Seerr is not configured or down.
func (s *Service) RequestIndex(ctx context.Context) map[string][]string {
	idx, err := s.seerr(ctx)
	if err != nil || idx == nil {
		return nil
	}
	return idx.byKey
}

// seerr returns the requests index. A stale index is served at once and
// refreshed behind; after a failure Seerr is left alone for a minute, so a
// hung Seerr does not stall every calendar request.
func (s *Service) seerr(ctx context.Context) (*seerrIndex, error) {
	s.mu.Lock()
	idx, age := s.seerrIdx, s.now().Sub(s.seerrTime)
	lastErr, errAge := s.seerrErr, s.now().Sub(s.seerrErrAt)
	s.mu.Unlock()
	if idx != nil && age < ttl[SourceSeerr] {
		return idx, nil
	}
	if lastErr != nil && errAge < seerrBackoff {
		if idx != nil {
			return idx, nil
		}
		return nil, lastErr
	}
	refresh := func() (any, error) { return s.buildSeerr(context.WithoutCancel(ctx)) }
	if idx != nil || noWait(ctx) {
		go func() { _, _, _ = s.sf.Do("seerr-index", refresh) }()
		if idx != nil {
			return idx, nil
		}
		return nil, ErrSyncing
	}
	v, err, _ := s.sf.Do("seerr-index", refresh)
	if err != nil {
		return nil, err
	}
	return v.(*seerrIndex), nil
}

// seerrBackoff is how long Seerr is not retried after a failed refresh.
const seerrBackoff = time.Minute

func (s *Service) buildSeerr(ctx context.Context) (res any, err error) {
	defer func() {
		if err != nil {
			s.mu.Lock()
			s.seerrErr, s.seerrErrAt = err, s.now()
			s.mu.Unlock()
		}
	}()

	clients, err := s.d.Seerrs(ctx)
	if err != nil {
		return nil, err
	}
	if len(clients) == 0 {
		return nil, ErrNoSeerr
	}
	idx := &seerrIndex{byKey: map[string][]string{}, byAccount: map[string]map[string]bool{},
		byName: map[string]map[string]bool{}, accounts: map[string]bool{}}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var errs []error
	for _, c := range clients {
		users, err := c.Users(cctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		byID := map[int64]seerr.User{}
		for _, u := range users {
			byID[u.ID] = u
			if a := u.Account(); a != "" {
				idx.accounts[a] = true
			}
		}
		reqs, err := c.AllRequests(cctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, r := range reqs {
			u := byID[r.RequestedBy.ID]
			name := u.DisplayName
			if name == "" {
				name = r.RequestedBy.DisplayName
			}
			account := u.Account()
			if account == "" {
				account = seerr.User{PlexID: r.RequestedBy.PlexID, JellyfinUserID: r.RequestedBy.JellyfinUserID}.Account()
			}
			for _, k := range r.MediaKeys() {
				if name != "" && !contains(idx.byKey[k], name) {
					idx.byKey[k] = append(idx.byKey[k], name)
				}
				if account != "" {
					add(idx.byAccount, account, k)
				}
				for _, n := range []string{u.PlexUsername, u.JellyfinUsername, u.Email} {
					if n != "" {
						addName(idx.byName, n, k)
					}
				}
			}
		}
	}
	if len(errs) == len(clients) {
		return nil, errors.Join(errs...)
	}
	s.mu.Lock()
	s.seerrIdx, s.seerrTime, s.seerrErr = idx, s.now(), nil
	s.mu.Unlock()
	return idx, nil
}

func add(m map[string]map[string]bool, id string, k string) {
	if m[id] == nil {
		m[id] = map[string]bool{}
	}
	m[id][k] = true
}

func addName(m map[string]map[string]bool, n, k string) {
	n = lower(n)
	if m[n] == nil {
		m[n] = map[string]bool{}
	}
	m[n][k] = true
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

// Result is a user's union of the requested sources.
type Result struct {
	// Keys may be a cached set shared with other requests: read only.
	Keys map[string]bool
	// Errors per source, as codes the UI translates (see Code).
	Errors map[string]string
}

// Code maps an error to a short code the UI translates.
func Code(err error) string {
	switch {
	case errors.Is(err, ErrNoSeerr):
		return "no_seerr"
	case errors.Is(err, ErrNoSeerrUser):
		return "no_seerr_user"
	case errors.Is(err, ErrNoPlexServer):
		return "no_plex_server"
	case errors.Is(err, ErrPlexSignedOut), errors.Is(err, plex.ErrUnauthorized):
		return "plex_signed_out"
	case errors.Is(err, ErrSyncing):
		return "syncing"
	default:
		return "unavailable"
	}
}

// For returns the union of the requested sources for user u.
// Peek is For without waiting on a sync: what is not ready yet comes back as
// ErrSyncing while the sync goes on. For background work (PERF-01).
func (s *Service) Peek(ctx context.Context, u store.User, sources []string) Result {
	return s.For(context.WithValue(ctx, noWaitKey{}, true), u, sources)
}

type noWaitKey struct{}

func noWait(ctx context.Context) bool { v, _ := ctx.Value(noWaitKey{}).(bool); return v }

func (s *Service) For(ctx context.Context, u store.User, sources []string) Result {
	res := Result{Keys: map[string]bool{}, Errors: map[string]string{}}
	if len(sources) == 1 && Valid(sources[0]) {
		// One source: hand its set over instead of copying thousands of keys.
		keys, err := s.source(ctx, u, sources[0])
		if err != nil {
			res.Errors[sources[0]] = Code(err)
		}
		if keys != nil {
			res.Keys = keys
		}
		return res
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, src := range sources {
		if !Valid(src) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys, err := s.source(ctx, u, src)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Errors[src] = Code(err)
			}
			for k := range keys {
				res.Keys[k] = true
			}
		}()
	}
	wg.Wait()
	return res
}

func (s *Service) source(ctx context.Context, u store.User, src string) (map[string]bool, error) {
	if src == SourceSeerr {
		idx, err := s.seerr(ctx)
		if err != nil {
			return nil, err
		}
		account := seerr.AccountKey(u.AccountID)
		if keys, ok := idx.byAccount[account]; ok {
			return keys, nil
		}
		for _, n := range []string{u.Username, u.Email} {
			if keys, ok := idx.byName[lower(n)]; ok && n != "" {
				return keys, nil
			}
		}
		if !idx.accounts[account] {
			return nil, ErrNoSeerrUser
		}
		return map[string]bool{}, nil
	}

	key := fmt.Sprintf("%d/%s", u.ID, src)
	set, at, syncErr, err := s.userMedia(ctx, u, src, key)
	if err != nil {
		return nil, err
	}
	var stored error
	if syncErr != "" {
		stored = errors.New(syncErr)
	}
	fresh := ttl[src]
	if stored != nil {
		fresh = seerrBackoff // a failed sync is retried soon, not after the full TTL
	}
	if set != nil && s.now().Sub(at) < fresh {
		return set, stored
	}
	sync := func() (any, error) {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		keys, ferr := s.fetch(cctx, u, src)
		now := s.now()
		if serr := s.d.Store.SetUserMedia(cctx, u.ID, src, keys, ferr, now); serr != nil {
			s.syncedMedia(key, nil) // the store may be ahead of the mirror
			return nil, serr
		}
		if ferr != nil {
			// The store keeps the previous set with the error: reread it.
			s.syncedMedia(key, nil)
			s.d.Log.Warn("personal sync", "user", u.Username, "source", src, "err", ferr)
			return nil, ferr
		}
		out := make(map[string]bool, len(keys))
		for _, k := range keys {
			out[k] = true
		}
		// Same second precision as the store.
		s.syncedMedia(key, &mediaEntry{createdAt: u.CreatedAt, set: out, at: time.Unix(now.Unix(), 0)})
		return out, nil
	}
	if set != nil && stored == nil {
		// Stale: answer with what we have, refresh behind.
		go func() { _, _, _ = s.sf.Do(key, sync) }()
		return set, nil
	}
	// First sync, or the last one failed (what we have may be empty): wait
	// for it, but not past the HTTP write timeout; a long history keeps
	// syncing in the background and shows up on a reload.
	ch := s.sf.DoChan(key, sync)
	if noWait(ctx) {
		return nil, ErrSyncing // the sync goes on; the caller retries later
	}
	select {
	case r := <-ch:
		if r.Err != nil {
			return set, r.Err
		}
		return r.Val.(map[string]bool), nil
	case <-time.After(firstSyncWait):
		return set, ErrSyncing
	}
}

// userMedia returns a synced source from the mirror, or from the store
// (after a restart or an eviction).
func (s *Service) userMedia(ctx context.Context, u store.User, src, key string) (map[string]bool, time.Time, string, error) {
	s.mu.Lock()
	e, ok := s.media[key]
	gen := s.mediaGen
	s.mu.Unlock()
	if ok && e.createdAt.Equal(u.CreatedAt) {
		return e.set, e.at, e.syncErr, nil
	}
	set, at, syncErr, err := s.d.Store.UserMedia(ctx, u.ID, src)
	if err == nil && set != nil {
		s.mu.Lock()
		if s.mediaGen == gen { // no sync wrote meanwhile: what we read is current
			s.media[key] = mediaEntry{createdAt: u.CreatedAt, set: set, at: at, syncErr: syncErr}
		}
		s.mu.Unlock()
	}
	return set, at, syncErr, err
}

// syncedMedia records a sync's outcome (nil: reread the store next time) and
// sweeps entries past their TTL, so inactive or deleted users do not stay in
// memory; an evicted entry is just reread from the store.
func (s *Service) syncedMedia(key string, e *mediaEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mediaGen++
	now := s.now()
	for k, old := range s.media {
		if now.Sub(old.at) >= ttl[k[strings.IndexByte(k, '/')+1:]] {
			delete(s.media, k)
		}
	}
	if e == nil {
		delete(s.media, key)
	} else {
		s.media[key] = *e
	}
}

// firstSyncWait bounds how long a request waits for a user's first sync.
const firstSyncWait = 45 * time.Second

func (s *Service) fetch(ctx context.Context, u store.User, src string) ([]string, error) {
	switch src {
	case SourceWatchlist:
		tok, err := s.d.UserToken(u)
		if err != nil {
			return nil, err
		}
		keys, err := s.d.PlexTV.Watchlist(ctx, tok)
		if errors.Is(err, plex.ErrUnauthorized) {
			return nil, ErrPlexSignedOut
		}
		return keys, err
	case SourceWatching, SourceWatched:
		srv, err := s.d.PlexServer(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNoPlexServer, err)
		}
		account, _ := strconv.ParseInt(u.AccountID, 10, 64) // a Plex account id
		if u.IsOwner {
			account = plex.OwnerAccountID
		}
		var since time.Time
		limit := 50000
		if src == SourceWatching {
			since = s.now().Add(-watchingWindow)
			limit = 5000
		}
		hist, err := srv.History(ctx, account, since, limit)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		shows := map[string]bool{} // ratingKeys of shows, for their TMDB keys
		var rks []string
		for _, h := range hist {
			if h.Type == "episode" {
				shows[h.RatingKey] = true
			}
			// "Shows in progress" are shows; movies watched are over.
			if src == SourceWatching && h.Type != "episode" {
				continue
			}
			if !seen[h.RatingKey] {
				seen[h.RatingKey] = true
				rks = append(rks, h.RatingKey)
			}
		}
		return s.resolve(ctx, srv, rks, shows)
	}
	return nil, fmt.Errorf("unknown source %q", src)
}

// resolve turns ratingKeys into external ids, through the plex_guids cache.
func (s *Service) resolve(ctx context.Context, srv *plex.Server, rks []string, shows map[string]bool) ([]string, error) {
	cached, err := s.d.Store.PlexGuids(ctx, rks)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, rk := range rks {
		if _, ok := cached[rk]; !ok {
			missing = append(missing, rk)
		}
	}
	if len(missing) > 0 {
		fetched, err := srv.Guids(ctx, missing)
		if err != nil && len(fetched) == 0 {
			return nil, err
		}
		for _, rk := range missing {
			if _, ok := fetched[rk]; !ok && err == nil {
				fetched[rk] = nil // deleted from Plex: remember it
			}
		}
		if err := s.d.Store.SavePlexGuids(ctx, fetched, s.now()); err != nil {
			return nil, err
		}
		for rk, ks := range fetched {
			cached[rk] = ks
		}
	}
	var out []string
	for _, rk := range rks {
		for _, k := range cached[rk] {
			if shows[rk] {
				k = plex.ShowKey(k)
			}
			out = append(out, k)
		}
	}
	return out, nil
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
