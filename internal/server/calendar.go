package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/neotoxicfr/agendarr/internal/arr"
	"github.com/neotoxicfr/agendarr/internal/calendar"
	"github.com/neotoxicfr/agendarr/internal/httpx"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// maxRange caps one /api/calendar request (a month view with its leading and
// trailing weeks is 42 days; the agenda loads 5 weeks at a time).
const maxRange = 62 * 24 * time.Hour

// calendarSources lists the enabled Radarr and Sonarr instances.
func (s *Server) calendarSources(ctx context.Context) ([]calendar.Source, error) {
	list, err := s.store.Instances(ctx)
	if err != nil {
		return nil, err
	}
	var out []calendar.Source
	for _, i := range list {
		if !i.Enabled || (i.Kind != store.KindRadarr && i.Kind != store.KindSonarr) {
			continue
		}
		key, err := s.box.Open(i.APIKey)
		if err != nil {
			s.log.Error("open api key", "instance", i.Name, "err", err)
			continue
		}
		out = append(out, calendar.Source{ID: i.ID, Kind: i.Kind, Name: i.Name, URL: i.URL, APIKey: key})
	}
	return out, nil
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "calendar", pageData{})
}

// handleCalendarAPI serves ?from=YYYY-MM-DD&to=YYYY-MM-DD.
func (s *Server) handleCalendarAPI(w http.ResponseWriter, r *http.Request) {
	from, err1 := time.Parse(time.DateOnly, r.URL.Query().Get("from"))
	to, err2 := time.Parse(time.DateOnly, r.URL.Query().Get("to"))
	if err := errors.Join(err1, err2); err != nil || to.Before(from) || to.Sub(from) > maxRange {
		http.Error(w, "want from and to as YYYY-MM-DD, at most 62 days apart", http.StatusBadRequest)
		return
	}
	res, err := s.cal.Range(r.Context(), from, to)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := calendarResponse{Result: res, Personal: map[string]string{}}
	// Who asked for it, from Seerr (cached index; nil when Seerr is absent).
	if idx := s.personal.RequestIndex(r.Context()); idx != nil {
		for i := range out.Events {
			out.Events[i].RequestedBy = requesters(idx, out.Events[i].Keys)
		}
	}
	// ?mine=seerr,watching,... keeps what matches any of the sources.
	if mine := cleanSources(strings.Split(r.URL.Query().Get("mine"), ",")); len(mine) > 0 {
		pr := s.personal.For(r.Context(), *currentUser(r), mine)
		out.Personal = pr.Errors
		out.Events = keepKeys(out.Events, pr.Keys)
	}
	s.addLinks(r.Context(), out.Events, currentUser(r).IsAdmin)
	writeJSON(w, http.StatusOK, out)
}

type calendarResponse struct {
	calendar.Result
	// Personal maps a personal source to the error that left it incomplete.
	Personal map[string]string `json:"personal"`
}

func requesters(idx map[string][]string, keys []string) []string {
	var out []string
	for _, k := range keys {
		for _, n := range idx[k] {
			dup := false
			for _, o := range out {
				dup = dup || o == n
			}
			if !dup {
				out = append(out, n)
			}
		}
	}
	return out
}

// Posters are cached on disk for a week: they rarely change, and a calendar
// page shows dozens.
const posterTTL = 7 * 24 * time.Hour

// handlePoster proxies /img/{instance}/{id}?w=250|500 to the instance's
// /api/v3/mediacover, so API keys never reach the browser.
func (s *Server) handlePoster(w http.ResponseWriter, r *http.Request) {
	instID, err1 := strconv.ParseInt(r.PathValue("inst"), 10, 64)
	mediaID, err2 := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err1 != nil || err2 != nil || mediaID <= 0 {
		http.NotFound(w, r)
		return
	}
	width := 250
	if r.URL.Query().Get("w") == "500" {
		width = 500
	}
	path := filepath.Join(s.cfg.DataDir, "cache", "posters", fmt.Sprintf("%d-%d-%d.jpg", instID, mediaID, width))
	if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < posterTTL {
		servePoster(w, r, path)
		return
	}
	_, err, _ := s.posterSF.Do(path, func() (any, error) {
		return nil, s.fetchPoster(r.Context(), instID, mediaID, width, path)
	})
	if err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			servePoster(w, r, path) // stale beats nothing
			return
		}
		if !errors.Is(err, errNoPoster) {
			s.log.Warn("poster", "instance", instID, "id", mediaID, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	servePoster(w, r, path)
}

var errNoPoster = errors.New("no poster")

func (s *Server) fetchPoster(ctx context.Context, instID, mediaID int64, width int, path string) error {
	inst, err := s.store.Instance(ctx, instID)
	if err != nil || !inst.Enabled || (inst.Kind != store.KindRadarr && inst.Kind != store.KindSonarr) {
		return errNoPoster
	}
	key, err := s.box.Open(inst.APIKey)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	c := httpx.Client{BaseURL: inst.URL, APIKey: key}
	res, err := c.Fetch(ctx, arr.PosterPath(mediaID, width))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return errNoPoster
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "image/") {
		return fmt.Errorf("HTTP %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".poster-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if _, err := io.Copy(tmp, io.LimitReader(res.Body, 8<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func servePoster(w http.ResponseWriter, r *http.Request, path string) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}
