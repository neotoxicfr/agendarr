// Package arr reads the calendars of Radarr and Sonarr (API v3).
package arr

import (
	"context"
	"fmt"
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

// HasPoster reports whether the item has a poster.
func HasPoster(images []Image) bool {
	for _, im := range images {
		if im.CoverType == "poster" && (im.URL != "" || im.RemoteURL != "") {
			return true
		}
	}
	return false
}

// PosterPath is the API path of a cached poster; width is 250 or 500.
func PosterPath(mediaID int64, width int) string {
	return fmt.Sprintf("/api/v3/mediacover/%d/poster-%d.jpg", mediaID, width)
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
	TitleSlug       string     `json:"titleSlug"`
	MovieFile       *File      `json:"movieFile"`
}

// File is the file Radarr or Sonarr has for a movie or an episode.
type File struct {
	Size      int64      `json:"size"`
	MediaInfo *MediaInfo `json:"mediaInfo"` // read from the file (ffprobe)
	Quality   struct {
		Quality struct {
			Source   string `json:"source"`   // webdl, webrip, bluray (Radarr); web, webRip, blurayRaw (Sonarr)
			Modifier string `json:"modifier"` // remux, brdisk (Radarr)
		} `json:"quality"`
	} `json:"quality"`
}

// MediaInfo is what Radarr and Sonarr read from the file. Languages are
// ISO 639-2 codes joined by "/" ("fre/eng").
type MediaInfo struct {
	AudioChannels         float64 `json:"audioChannels"`
	AudioCodec            string  `json:"audioCodec"`
	AudioLanguages        string  `json:"audioLanguages"`
	VideoBitDepth         int     `json:"videoBitDepth"`
	VideoCodec            string  `json:"videoCodec"`
	VideoDynamicRangeType string  `json:"videoDynamicRangeType"`
	Resolution            string  `json:"resolution"` // 3840x1606
	Subtitles             string  `json:"subtitles"`
}

// Series is the series embedded in a Sonarr calendar entry.
type Series struct {
	ID        int64   `json:"id"`
	Title     string  `json:"title"`
	Year      int     `json:"year"`
	TvdbID    int64   `json:"tvdbId"`
	TmdbID    int64   `json:"tmdbId"`
	ImdbID    string  `json:"imdbId"`
	Network   string  `json:"network"`
	TitleSlug string  `json:"titleSlug"`
	Overview  string  `json:"overview"`
	Runtime   int     `json:"runtime"`
	Images    []Image `json:"images"`
	// Library fields (/api/v3/series), absent from calendar entries.
	Status     string     `json:"status"` // continuing | ended | upcoming
	Monitored  bool       `json:"monitored"`
	NextAiring *time.Time `json:"nextAiring"`
	Statistics struct {
		EpisodeFileCount int `json:"episodeFileCount"`
	} `json:"statistics"`
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
	EpisodeFile   *File      `json:"episodeFile"`
	TvdbID        int64      `json:"tvdbId"` // of the episode
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

// AllMovies returns every movie of a Radarr instance.
func (c *Client) AllMovies(ctx context.Context) ([]Movie, error) {
	var out []Movie
	err := c.Get(ctx, "/api/v3/movie", nil, &out)
	return out, err
}

// AllSeries returns every series of a Sonarr instance.
func (c *Client) AllSeries(ctx context.Context) ([]Series, error) {
	var out []Series
	err := c.Get(ctx, "/api/v3/series", nil, &out)
	return out, err
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
	q.Set("includeEpisodeFile", "true")
	var out []Episode
	err := c.Get(ctx, "/api/v3/calendar", q, &out)
	return out, err
}
