package server

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// statusNext is how many upcoming releases the status lists.
const statusNext = 5

// handleStatus sums up the calendar for a dashboard tile, server side and
// without a session: releases today and in the next seven days, the
// instances that do not answer, the next releases. It exists only when
// AGENDARR_STATUS_TOKEN is set, and asks for that key (Bearer or X-Api-Key).
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if s.cfg.StatusToken == "" {
		http.NotFound(w, r)
		return
	}
	key := r.Header.Get("X-Api-Key")
	if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		key = b
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.StatusToken)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	now := time.Now()
	today := now.Format(time.DateOnly)
	weekEnd := now.AddDate(0, 0, 6).Format(time.DateOnly)
	res, err := s.cal.Range(r.Context(), now, now.AddDate(0, 0, 6))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := struct {
		Version        string   `json:"version"`
		Today          int      `json:"today"`
		AvailableToday int      `json:"available_today"`
		Week           int      `json:"week"`
		Failing        []string `json:"failing"`
		Next           []string `json:"next"`
	}{Version: Version, Failing: res.Failing, Next: []string{}}
	if out.Failing == nil {
		out.Failing = []string{}
	}
	events := res.Events
	sort.SliceStable(events, func(i, j int) bool { return events[i].Date < events[j].Date })
	for _, e := range events {
		if e.Date < today || e.Date > weekEnd {
			continue
		}
		out.Week++
		if e.Date == today {
			out.Today++
			if e.HasFile {
				out.AvailableToday++
			}
		}
		if len(out.Next) < statusNext && !e.HasFile {
			name := e.Title
			if e.Kind == "episode" {
				name += fmt.Sprintf(" S%02dE%02d", e.Season, e.Episode)
			}
			out.Next = append(out.Next, e.Date+" "+name)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
