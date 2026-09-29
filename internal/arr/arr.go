// Package arr reads the calendars of Radarr and Sonarr (API v3).
package arr

import (
	"context"
	"net/url"
	"time"

	"github.com/neotoxicfr/agendarr/internal/httpx"
)

// Status is /api/v3/system/status, used to test an instance.
type Status struct {
	AppName string `json:"appName"`
	Version string `json:"version"`
}

// Image is a poster, fanart or banner.
type Image struct {
	CoverType string `json:"coverType"`
	URL       string `json:"url"`       // local, e.g. /MediaCover/12/poster.jpg?lastWrite=...
	RemoteURL string `json:"remoteUrl"` // TMDB/TVDB CDN
}

// Poster returns the local poster path, if any.
func Poster(images []Image) string {
	for _, im := range images {
		if im.CoverType == "poster" && im.URL != "" {
			return im.URL
		}
	}
	return ""
}

// Movie is a Radarr calendar entry.
type Movie struct {
	ID              int64      `json:"id"`
	Title           string     `json:"title"`
	Year            int        `json:"year"`
	TmdbID          int64      `json:"tmdbId"`
	ImdbID          string     `json:"imdbId"`
	Overview        string     `json:"overview"`
	Runtime         int        `json:"runtime"`
	InCinemas       *time.Time `json:"inCinemas"`
	DigitalRelease  *time.Time `json:"digitalRelease"`
	PhysicalRelease *time.Time `json:"physicalRelease"`
	HasFile         bool       `json:"hasFile"`
	Monitored       bool       `json:"monitored"`
	Genres          []string   `json:"genres"`
	Images          []Image    `json:"images"`
}

// Series is the series embedded in a Sonarr calendar entry.
type Series struct {
	ID       int64   `json:"id"`
	Title    string  `json:"title"`
	Year     int     `json:"year"`
	TvdbID   int64   `json:"tvdbId"`
	TmdbID   int64   `json:"tmdbId"`
	ImdbID   string  `json:"imdbId"`
	Network  string  `json:"network"`
	Overview string  `json:"overview"`
	Runtime  int     `json:"runtime"`
	Images   []Image `json:"images"`
}

// Episode is a Sonarr calendar entry.
type Episode struct {
	ID            int64      `json:"id"`
	SeriesID      int64      `json:"seriesId"`
	SeasonNumber  int        `json:"seasonNumber"`
	EpisodeNumber int        `json:"episodeNumber"`
	Title         string     `json:"title"`
	Overview      string     `json:"overview"`
	AirDate       string     `json:"airDate"` // local date at the network, YYYY-MM-DD
	AirDateUTC    *time.Time `json:"airDateUtc"`
	Runtime       int        `json:"runtime"`
	HasFile       bool       `json:"hasFile"`
	Monitored     bool       `json:"monitored"`
	FinaleType    string     `json:"finaleType"`
	Series        Series     `json:"series"`
}

// Client is one Radarr or Sonarr instance.
type Client struct{ httpx.Client }

// New returns a client for baseURL.
func New(baseURL, apiKey string) *Client {
	return &Client{httpx.Client{BaseURL: baseURL, APIKey: apiKey}}
}

// Status checks the URL and API key.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.Get(ctx, "/api/v3/system/status", nil, &s)
	return s, err
}

func window(start, end time.Time) url.Values {
	q := url.Values{}
	q.Set("start", start.UTC().Format(time.RFC3339))
	q.Set("end", end.UTC().Format(time.RFC3339))
	q.Set("unmonitored", "true")
	return q
}

// Movies returns the Radarr calendar between start and end.
func (c *Client) Movies(ctx context.Context, start, end time.Time) ([]Movie, error) {
	var out []Movie
	err := c.Get(ctx, "/api/v3/calendar", window(start, end), &out)
	return out, err
}

// Episodes returns the Sonarr calendar between start and end, with series.
func (c *Client) Episodes(ctx context.Context, start, end time.Time) ([]Episode, error) {
	q := window(start, end)
	q.Set("includeSeries", "true")
	var out []Episode
	err := c.Get(ctx, "/api/v3/calendar", q, &out)
	return out, err
}
