package server

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/neotoxicfr/agendarr/internal/calendar"
	"github.com/neotoxicfr/agendarr/internal/ics"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// The feed spans a month back (recent releases stay visible) and four ahead.
const (
	feedBack  = 30 * 24 * time.Hour
	feedAhead = 120 * 24 * time.Hour
)

type syncLinks struct {
	HTTPS, Google, Outlook string
	// Webcal is built here from our own base URL; html/template would
	// otherwise replace the unknown webcal: scheme with #ZgotmplZ.
	Webcal  template.URL
	Created time.Time
}

// feedLinks returns the subscription URLs for the user's token, if any.
func (s *Server) feedLinks(r *http.Request, u *store.User) (*syncLinks, error) {
	sealed, created, err := s.store.ICSToken(r.Context(), u.ID)
	if err != nil || sealed == "" {
		return nil, err
	}
	token, err := s.box.Open(sealed)
	if err != nil {
		return nil, err
	}
	feed := s.baseURL(r) + "/ics/" + token + ".ics?lang=" + s.i18n.Pick(r)
	webcal := "webcal://" + strings.TrimPrefix(strings.TrimPrefix(feed, "https://"), "http://")
	return &syncLinks{
		HTTPS:   feed,
		Webcal:  template.URL(webcal), //nolint:gosec // our own URL, see syncLinks
		Google:  "https://calendar.google.com/calendar/r?cid=" + url.QueryEscape(webcal),
		Outlook: "https://outlook.live.com/calendar/0/addfromweb?name=Agendarr&url=" + url.QueryEscape(feed),
		Created: created,
	}, nil
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	links, err := s.feedLinks(r, currentUser(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "sync", pageData{Title: "title.sync", Data: links})
}

func (s *Server) handleSyncNew(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	if err := s.store.SetICSToken(r.Context(), currentUser(r).ID, token, s.box.Seal(token), time.Now()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.flash(r, "sync.created")
	http.Redirect(w, r, "/sync", http.StatusSeeOther)
}

func (s *Server) handleSyncRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteICSToken(r.Context(), currentUser(r).ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.flash(r, "sync.revoked")
	http.Redirect(w, r, "/sync", http.StatusSeeOther)
}

// handleSyncQR draws the webcal link, for adding the feed from a phone.
func (s *Server) handleSyncQR(w http.ResponseWriter, r *http.Request) {
	links, err := s.feedLinks(r, currentUser(r))
	if err != nil || links == nil {
		http.NotFound(w, r)
		return
	}
	code, err := qr.Encode(string(links.Webcal), qr.M)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	code.Scale = 6
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(code.PNG())
}

// handleFeed serves /ics/{token}.ics. No session: calendar apps only have
// the URL, so the token is the credential.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutSuffix(r.PathValue("file"), ".ics")
	if !ok || len(token) < 40 {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	u, err := s.store.UserByICSToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := time.Now()
	res, err := s.cal.Range(ctx, now.Add(-feedBack), now.Add(feedAhead))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	prefs := s.loadPrefs(ctx, u.ID)
	events := filterEvents(res.Events, prefs.Filters)
	if len(prefs.Mine) > 0 {
		pr := s.personal.For(ctx, u, prefs.Mine)
		events = keepKeys(events, pr.Keys)
	}
	if idx := s.personal.RequestIndex(ctx); idx != nil {
		for i := range events {
			events[i].RequestedBy = requesters(idx, events[i].Keys)
		}
	}
	lang := r.URL.Query().Get("lang")
	if !s.i18n.Has(lang) {
		lang = s.i18n.Pick(r)
	}
	T := s.i18n.Map(lang)
	labels := ics.Labels{
		CalName: "Agendarr",
		Release: map[string]string{
			calendar.ReleaseCinema:   T["cal.release_cinema"],
			calendar.ReleaseDigital:  T["cal.release_digital"],
			calendar.ReleasePhysical: T["cal.release_physical"],
		},
		Available: T["cal.status_downloaded"], Expected: T["cal.status_expected"],
		Unplanned: T["cal.status_unmonitored"], RequestedBy: T["sync.feed_requested_by"],
	}
	var buf bytes.Buffer
	if err := ics.Write(&buf, events, labels, now); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="agendarr.ics"`)
	w.Header().Set("Cache-Control", "private, max-age=900")
	_, _ = buf.WriteTo(w)
}

// filterEvents applies the filter panel's settings, as the browser does.
func filterEvents(in []calendar.Event, f PrefFilters) []calendar.Event {
	out := make([]calendar.Event, 0, len(in))
	for _, e := range in {
		if (e.Kind == "movie" && !f.Movies) || (e.Kind == "episode" && !f.Episodes) ||
			(f.HideDownloaded && e.HasFile) || (f.HideUnmonitored && !e.Monitored) {
			continue
		}
		hidden := len(e.Instances) > 0
		for _, n := range e.Instances {
			if v, ok := f.Instances[n]; !ok || v {
				hidden = false
			}
		}
		if !hidden {
			out = append(out, e)
		}
	}
	return out
}

func keepKeys(in []calendar.Event, keys map[string]bool) []calendar.Event {
	out := in[:0]
	for _, e := range in {
		for _, k := range e.Keys {
			if keys[k] {
				out = append(out, e)
				break
			}
		}
	}
	return out
}
