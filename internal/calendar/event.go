// Package calendar merges the Radarr and Sonarr calendars into one list of
// release events, cached per instance and per month.
package calendar

import (
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/neotoxicfr/agendarr/internal/arr"
)

// Release types of a movie event.
const (
	ReleaseCinema   = "cinema"
	ReleaseDigital  = "digital"
	ReleasePhysical = "physical"
)

// Event is one dated release, as sent to the browser.
type Event struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`              // movie | episode
	Release  string `json:"release,omitempty"` // movies: cinema | digital | physical
	Title    string `json:"title"`             // movie or series title
	Year     int    `json:"year,omitempty"`
	Season   int    `json:"season"` // not omitempty: 0 is the specials season
	Episode  int    `json:"episode"`
	EpTitle  string `json:"epTitle,omitempty"`
	Finale   string `json:"finale,omitempty"` // series | season | midseason
	Premiere bool   `json:"premiere,omitempty"`
	// Date is the calendar day for movies (no time of day), YYYY-MM-DD.
	Date string `json:"date"`
	// Air is the exact air time of an episode, UTC; the browser places it
	// on the viewer's local day.
	Air       *time.Time `json:"air,omitempty"`
	Runtime   int        `json:"runtime,omitempty"`
	HasFile   bool       `json:"hasFile"`
	Monitored bool       `json:"monitored"`
	Network   string     `json:"network,omitempty"`
	Overview  string     `json:"overview,omitempty"`
	Genres    []string   `json:"genres,omitempty"`
	Poster    string     `json:"poster,omitempty"` // proxied: /img/{instance}/{movie or series id}
	Instances []string   `json:"instances"`
	// Keys are the external ids of the movie or series ("tmdb:1", "tvdb:2",
	// "imdb:tt3"), used by the personal filters.
	Keys []string `json:"-"` // server-side only: the browser never reads them
	// Group identifies the movie or series across events.
	Group string `json:"group"`
	// RequestedBy lists the Seerr users who requested it (set per response).
	RequestedBy []string `json:"requestedBy,omitempty"`
	// Media describes the file of each instance that has one.
	Media []Media `json:"media,omitempty"`
	// Arr locates the movie or series in each instance (admin links).
	Arr []ArrRef `json:"-"`
	// PlexURL opens it on the Plex server (set per response).
	PlexURL string `json:"plexUrl,omitempty"`
	// ArrLinks open it in Radarr or Sonarr, for admins (set per response).
	ArrLinks []Link `json:"arrLinks,omitempty"`
	// Ext opens it on TMDB, TVDB and IMDb.
	Ext []Link `json:"ext,omitempty"`
}

// Media sums up one file: quality, video, audio and subtitles.
type Media struct {
	Instance   string   `json:"instance"`
	Resolution string   `json:"resolution,omitempty"`
	VideoCodec string   `json:"videoCodec,omitempty"` // x265
	BitDepth   int      `json:"bitDepth,omitempty"`
	HDR        string   `json:"hdr,omitempty"`        // DV HDR10Plus
	AudioCodec string   `json:"audioCodec,omitempty"` // EAC3 Atmos
	Channels   string   `json:"channels,omitempty"`   // 5.1
	AudioLangs []string `json:"audioLangs,omitempty"`
	Subs       []string `json:"subs,omitempty"`
	Size       int64    `json:"size,omitempty"`
}

// ArrRef is where a movie or series lives in one instance's web UI.
type ArrRef struct {
	InstanceID int64
	Instance   string
	Path       string // /movie/{slug} or /series/{slug}
}

// Link is a labelled URL. ID names the site of an external link (tmdb,
// tvdb, imdb, letterboxd), which each user can hide.
type Link struct {
	ID    string `json:"id,omitempty"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Sites lists the sites of external links, in display order.
var Sites = []string{"tmdb", "tvdb", "imdb", "letterboxd", "mdblist"}

// sortKey orders events within a day: timed episodes by air time, then
// movies, by title.
func (e Event) sortKey() string {
	t := "99"
	if e.Air != nil {
		t = e.Air.UTC().Format("15:04")
	}
	return e.Date + t + strings.ToLower(e.Title) + fmt.Sprintf("%03d%03d", e.Season, e.Episode)
}

func keys(tmdb, tvdb int64, imdb string) []string {
	var out []string
	if tvdb != 0 {
		out = append(out, fmt.Sprintf("tvdb:%d", tvdb))
	}
	if tmdb != 0 {
		out = append(out, fmt.Sprintf("tmdb:%d", tmdb))
	}
	if imdb != "" {
		out = append(out, "imdb:"+imdb)
	}
	return out
}

// posterURL points at the poster proxy. Radarr 6 and Sonarr 4 no longer put
// local paths in calendar images, and /MediaCover needs a browser session;
// /api/v3/mediacover/{id} accepts the API key.
func posterURL(instID, mediaID int64, images []arr.Image) string {
	if mediaID == 0 || !arr.HasPoster(images) {
		return ""
	}
	return fmt.Sprintf("/img/%d/%d", instID, mediaID)
}

// fromMovie turns one Radarr movie into up to three events.
func fromMovie(instID int64, instName string, m arr.Movie) []Event {
	base := Event{
		Kind: "movie", Title: m.Title, Year: m.Year, Runtime: m.Runtime,
		HasFile: m.HasFile, Monitored: m.Monitored, Overview: m.Overview, Genres: m.Genres,
		Poster: posterURL(instID, m.ID, m.Images), Instances: []string{instName},
		Keys: keys(m.TmdbID, 0, m.ImdbID),
	}
	if m.HasFile {
		base.Media = mediaOf(instName, m.MovieFile)
	}
	if m.TmdbID != 0 {
		base.Ext = append(base.Ext, Link{"tmdb", "TMDB", fmt.Sprintf("https://www.themoviedb.org/movie/%d", m.TmdbID)})
	}
	if m.ImdbID != "" {
		base.Ext = append(base.Ext, Link{"imdb", "IMDb", "https://www.imdb.com/title/" + url.PathEscape(m.ImdbID) + "/"})
	}
	if m.TmdbID != 0 {
		base.Ext = append(base.Ext, Link{"letterboxd", "Letterboxd", fmt.Sprintf("https://letterboxd.com/tmdb/%d/", m.TmdbID)})
	}
	if m.ImdbID != "" {
		base.Ext = append(base.Ext, Link{"mdblist", "MDBList", "https://mdblist.com/movie/" + url.PathEscape(m.ImdbID)})
	}
	if m.TitleSlug != "" {
		base.Arr = []ArrRef{{InstanceID: instID, Instance: instName, Path: "/movie/" + url.PathEscape(m.TitleSlug)}}
	}
	base.Group = "movie:" + firstKey(base.Keys, fmt.Sprintf("radarr%d-%d", instID, m.ID))
	var out []Event
	for _, r := range []struct {
		kind string
		at   *time.Time
	}{{ReleaseCinema, m.InCinemas}, {ReleaseDigital, m.DigitalRelease}, {ReleasePhysical, m.PhysicalRelease}} {
		if r.at == nil || r.at.IsZero() {
			continue
		}
		e := base
		e.Release = r.kind
		// Radarr stores release days as midnight UTC: the UTC date is the day.
		e.Date = r.at.UTC().Format(time.DateOnly)
		e.ID = e.Group + ":" + r.kind
		out = append(out, e)
	}
	return out
}

// fromEpisode turns one Sonarr episode into an event.
func fromEpisode(instID int64, instName string, ep arr.Episode) (Event, bool) {
	s := ep.Series
	e := Event{
		Kind: "episode", Title: s.Title, Year: s.Year, Season: ep.SeasonNumber, Episode: ep.EpisodeNumber,
		EpTitle: ep.Title, Finale: ep.FinaleType, Premiere: ep.EpisodeNumber == 1,
		Runtime: ep.Runtime, HasFile: ep.HasFile, Monitored: ep.Monitored, Network: s.Network,
		Overview: ep.Overview, Poster: posterURL(instID, s.ID, s.Images), Instances: []string{instName},
		Keys: keys(s.TmdbID, s.TvdbID, s.ImdbID),
	}
	if ep.HasFile {
		e.Media = mediaOf(instName, ep.EpisodeFile)
	}
	// TMDB and TVDB open the episode itself; IMDb, the series.
	if s.TmdbID != 0 {
		e.Ext = append(e.Ext, Link{"tmdb", "TMDB", fmt.Sprintf("https://www.themoviedb.org/tv/%d/season/%d/episode/%d", s.TmdbID, ep.SeasonNumber, ep.EpisodeNumber)})
	}
	switch {
	case ep.TvdbID != 0:
		e.Ext = append(e.Ext, Link{"tvdb", "TVDB", fmt.Sprintf("https://thetvdb.com/dereferrer/episode/%d", ep.TvdbID)})
	case s.TvdbID != 0:
		e.Ext = append(e.Ext, Link{"tvdb", "TVDB", fmt.Sprintf("https://thetvdb.com/dereferrer/series/%d", s.TvdbID)})
	}
	if s.ImdbID != "" {
		e.Ext = append(e.Ext, Link{"imdb", "IMDb", "https://www.imdb.com/title/" + url.PathEscape(s.ImdbID) + "/"},
			Link{"mdblist", "MDBList", "https://mdblist.com/show/" + url.PathEscape(s.ImdbID)})
	}
	if s.TitleSlug != "" {
		e.Arr = []ArrRef{{InstanceID: instID, Instance: instName, Path: "/series/" + url.PathEscape(s.TitleSlug)}}
	}
	if e.Runtime == 0 {
		e.Runtime = s.Runtime
	}
	switch {
	case ep.AirDateUTC != nil && !ep.AirDateUTC.IsZero():
		t := ep.AirDateUTC.UTC()
		e.Air = &t
		e.Date = t.Format(time.DateOnly)
	case ep.AirDate != "":
		e.Date = ep.AirDate
	default:
		return e, false
	}
	e.Group = "series:" + firstKey(e.Keys, fmt.Sprintf("sonarr%d-%d", instID, s.ID))
	e.ID = fmt.Sprintf("%s:s%02de%02d", e.Group, ep.SeasonNumber, ep.EpisodeNumber)
	return e, true
}

func firstKey(k []string, fallback string) string {
	if len(k) > 0 {
		return k[0]
	}
	return fallback
}

// merge folds duplicates (the same release seen by Radarr and Radarr 4K)
// into one event listing both instances, and sorts the result.
func merge(events []Event) []Event {
	byID := make(map[string]int, len(events))
	out := make([]Event, 0, len(events))
	for _, e := range events {
		if i, ok := byID[e.ID]; ok {
			m := &out[i]
			m.HasFile = m.HasFile || e.HasFile
			m.Monitored = m.Monitored || e.Monitored
			for _, n := range e.Instances {
				if !contains(m.Instances, n) {
					m.Instances = append(m.Instances, n)
				}
			}
			if m.Poster == "" {
				m.Poster = e.Poster
			}
			// A month fetch also returns the other release dates of a movie,
			// so the same instance can come twice: add each one once.
			for _, x := range e.Media {
				if !slices.ContainsFunc(m.Media, func(y Media) bool { return y.Instance == x.Instance }) {
					m.Media = append(slices.Clip(m.Media), x)
				}
			}
			for _, x := range e.Arr {
				if !slices.ContainsFunc(m.Arr, func(y ArrRef) bool { return y.InstanceID == x.InstanceID }) {
					m.Arr = append(slices.Clip(m.Arr), x)
				}
			}
			continue
		}
		byID[e.ID] = len(out)
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].sortKey() < out[j].sortKey() })
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
