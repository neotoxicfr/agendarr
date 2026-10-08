package calendar

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/neotoxicfr/agendarr/internal/arr"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// TTL is how long a fetched month is served without refetching. Past it the
// cached month is still served at once while a refresh runs in background.
const TTL = 5 * time.Minute

// Source is an enabled Radarr or Sonarr instance with its key opened.
type Source struct {
	ID     int64
	Kind   string
	Name   string
	URL    string
	APIKey string
}

// Sources lists the instances to read; the server provides it.
type Sources func(ctx context.Context) ([]Source, error)

type monthKey struct {
	inst  int64
	month string // 2026-10
}

type entry struct {
	events  []Event
	fetched time.Time
	err     error
}

// Service serves merged events.
type Service struct {
	sources Sources
	log     *slog.Logger
	now     func() time.Time

	mu    sync.Mutex
	cache map[monthKey]entry
	gen   int // bumped by Invalidate; stale refreshes are dropped
	sf    singleflight.Group

	// fetch is overridable in tests.
	fetch func(ctx context.Context, s Source, from, to time.Time) ([]Event, error)
}

// New returns a Service.
func New(sources Sources, log *slog.Logger) *Service {
	return &Service{sources: sources, log: log, now: time.Now, cache: map[monthKey]entry{}, fetch: fetchSource}
}

// Invalidate drops the cache, after an instance changed.
func (s *Service) Invalidate() {
	s.mu.Lock()
	s.cache = map[monthKey]entry{}
	s.gen++
	s.mu.Unlock()
}

// Result is a merged calendar range.
type Result struct {
	Events []Event `json:"events"`
	// Failing names the instances that could not be read; their events are
	// missing or come from an older fetch.
	Failing []string `json:"failing"`
	// Configured is false when no Radarr or Sonarr instance is enabled.
	Configured bool `json:"configured"`
}

// Range returns the events whose day lies in [from, to] (dates, inclusive).
// Episode days are UTC here; the browser re-buckets by local time, so one
// day of margin is fetched on each side.
func (s *Service) Range(ctx context.Context, from, to time.Time) (Result, error) {
	srcs, err := s.sources(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{Events: []Event{}, Failing: []string{}, Configured: len(srcs) > 0}
	lo := from.AddDate(0, 0, -1).Format(time.DateOnly)
	hi := to.AddDate(0, 0, 1).Format(time.DateOnly)
	var all []Event
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, src := range srcs {
		for _, m := range months(from.AddDate(0, 0, -1), to.AddDate(0, 0, 1)) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ev, err := s.month(ctx, src, m)
				mu.Lock()
				defer mu.Unlock()
				if err != nil && !contains(res.Failing, src.Name) {
					res.Failing = append(res.Failing, src.Name)
				}
				for _, e := range ev {
					if e.Date >= lo && e.Date <= hi {
						all = append(all, e)
					}
				}
			}()
		}
	}
	wg.Wait()
	res.Events = merge(all)
	if res.Events == nil {
		res.Events = []Event{}
	}
	return res, nil
}

// month returns one instance-month, fresh or cached.
func (s *Service) month(ctx context.Context, src Source, month time.Time) ([]Event, error) {
	k := monthKey{src.ID, month.Format("2006-01")}
	s.mu.Lock()
	e, ok := s.cache[k]
	gen := s.gen
	s.mu.Unlock()

	fresh := ok && s.now().Sub(e.fetched) < TTL
	if fresh {
		return e.events, e.err
	}
	load := func() (any, error) {
		start := month
		end := month.AddDate(0, 1, 0).Add(-time.Second)
		ev, err := s.fetch(context.WithoutCancel(ctx), src, start, end)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.gen != gen {
			return ev, err // the instance changed meanwhile: do not cache
		}
		if err != nil {
			s.log.Warn("calendar fetch", "instance", src.Name, "month", k.month, "err", err)
			// Keep serving what we had, flagged as failing; retry after TTL.
			old := s.cache[k]
			s.cache[k] = entry{events: old.events, fetched: s.now(), err: err}
			return old.events, err
		}
		s.cache[k] = entry{events: ev, fetched: s.now()}
		return ev, nil
	}
	sfKey := fmt.Sprintf("%d/%s/%d", k.inst, k.month, gen)
	if ok {
		// Stale: answer now with the cached month, refresh behind.
		go func() { _, _, _ = s.sf.Do(sfKey, load) }()
		return e.events, e.err
	}
	v, err, _ := s.sf.Do(sfKey, load)
	ev, _ := v.([]Event)
	return ev, err
}

func months(from, to time.Time) []time.Time {
	var out []time.Time
	m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
	for !m.After(to) {
		out = append(out, m)
		m = m.AddDate(0, 1, 0)
	}
	return out
}

func fetchSource(ctx context.Context, src Source, from, to time.Time) ([]Event, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := arr.New(src.URL, src.APIKey)
	var out []Event
	switch src.Kind {
	case store.KindRadarr:
		movies, err := c.Movies(ctx, from, to)
		if err != nil {
			return nil, err
		}
		for _, m := range movies {
			out = append(out, fromMovie(src.ID, src.Name, m)...)
		}
	case store.KindSonarr:
		eps, err := c.Episodes(ctx, from, to)
		if err != nil {
			return nil, err
		}
		for _, ep := range eps {
			if e, ok := fromEpisode(src.ID, src.Name, ep); ok {
				out = append(out, e)
			}
		}
	}
	return out, nil
}
