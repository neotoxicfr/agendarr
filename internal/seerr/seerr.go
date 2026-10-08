// Package seerr reads Seerr (and its predecessors Overseerr and Jellyseerr,
// which share the API): users, requests, and the Radarr/Sonarr servers it
// knows about.
package seerr

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/neotoxicfr/agendarr/internal/httpx"
)

// Client is one Seerr instance.
type Client struct{ httpx.Client }

// New returns a client for baseURL.
func New(baseURL, apiKey string) *Client {
	return &Client{httpx.Client{BaseURL: baseURL, APIKey: apiKey}}
}

// Status is /api/v1/status.
type Status struct {
	Version string `json:"version"`
}

// Me is the account behind the API key (the Seerr owner).
type Me struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName"`
}

// Test checks the URL (status) and the API key (auth/me).
func (c *Client) Test(ctx context.Context) (Status, error) {
	var s Status
	if err := c.Get(ctx, "/api/v1/status", nil, &s); err != nil {
		return s, err
	}
	var me Me
	if err := c.Get(ctx, "/api/v1/auth/me", nil, &me); err != nil {
		return s, err
	}
	return s, nil
}

// User is a Seerr user; PlexID links it to a Plex account.
type User struct {
	ID           int64  `json:"id"`
	PlexID       int64  `json:"plexId"`
	PlexUsername string `json:"plexUsername"`
	Email        string `json:"email"`
	DisplayName  string `json:"displayName"`
}

// Users lists every Seerr user.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var all []User
	for skip := 0; ; {
		var page struct {
			PageInfo struct {
				Pages   int `json:"pages"`
				Page    int `json:"page"`
				Results int `json:"results"`
			} `json:"pageInfo"`
			Results []User `json:"results"`
		}
		q := url.Values{"take": {"100"}, "skip": {strconv.Itoa(skip)}}
		if err := c.Get(ctx, "/api/v1/user", q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		skip += len(page.Results)
		if len(page.Results) == 0 || skip >= page.PageInfo.Results {
			return all, nil
		}
	}
}

// UserForPlex finds the Seerr user linked to a Plex account.
func UserForPlex(users []User, plexID int64, plexUsername, email string) (User, bool) {
	for _, u := range users {
		if plexID != 0 && u.PlexID == plexID {
			return u, true
		}
	}
	// Users imported before Seerr stored the Plex id: match by name or email.
	for _, u := range users {
		if (plexUsername != "" && strings.EqualFold(u.PlexUsername, plexUsername)) ||
			(email != "" && strings.EqualFold(u.Email, email)) {
			return u, true
		}
	}
	return User{}, false
}

// Request is a media request.
type Request struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"` // movie | tv
	RequestedBy struct {
		ID          int64  `json:"id"`
		DisplayName string `json:"displayName"`
		PlexID      int64  `json:"plexId"`
	} `json:"requestedBy"`
	Media struct {
		TmdbID    int64  `json:"tmdbId"`
		TvdbID    int64  `json:"tvdbId"`
		MediaType string `json:"mediaType"`
	} `json:"media"`
}

// MediaKeys returns the external ids of a request ("tmdb:1", "tvdb:2").
func (r Request) MediaKeys() []string {
	var out []string
	if r.Media.TmdbID != 0 {
		out = append(out, fmt.Sprintf("tmdb:%d", r.Media.TmdbID))
	}
	if r.Media.TvdbID != 0 {
		out = append(out, fmt.Sprintf("tvdb:%d", r.Media.TvdbID))
	}
	return out
}

// RequestsBy lists every request made by a Seerr user. The requestedBy filter
// is applied server-side where supported and re-checked here for older
// versions that ignore it.
func (c *Client) RequestsBy(ctx context.Context, userID int64) ([]Request, error) {
	var out []Request
	for skip := 0; ; {
		var page struct {
			PageInfo struct {
				Results int `json:"results"`
			} `json:"pageInfo"`
			Results []Request `json:"results"`
		}
		q := url.Values{
			"take": {"100"}, "skip": {strconv.Itoa(skip)}, "filter": {"all"},
			"sort": {"added"}, "requestedBy": {strconv.FormatInt(userID, 10)},
		}
		if err := c.Get(ctx, "/api/v1/request", q, &page); err != nil {
			return nil, err
		}
		for _, r := range page.Results {
			if r.RequestedBy.ID == userID {
				out = append(out, r)
			}
		}
		skip += len(page.Results)
		if len(page.Results) == 0 || skip >= page.PageInfo.Results || skip >= 10000 {
			return out, nil
		}
	}
}

// AllRequests lists every request (a household has a few hundred), which
// serves both "my requests" and "requested by" without a call per user.
func (c *Client) AllRequests(ctx context.Context) ([]Request, error) {
	var out []Request
	for skip := 0; skip < 20000; {
		var page struct {
			PageInfo struct {
				Results int `json:"results"`
			} `json:"pageInfo"`
			Results []Request `json:"results"`
		}
		q := url.Values{"take": {"100"}, "skip": {strconv.Itoa(skip)}, "filter": {"all"}, "sort": {"added"}}
		if err := c.Get(ctx, "/api/v1/request", q, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Results...)
		skip += len(page.Results)
		if len(page.Results) == 0 || skip >= page.PageInfo.Results {
			break
		}
	}
	return out, nil
}

// ArrServer is a Radarr or Sonarr server configured in Seerr.
type ArrServer struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Hostname    string `json:"hostname"`
	Port        int    `json:"port"`
	APIKey      string `json:"apiKey"`
	UseSSL      bool   `json:"useSsl"`
	BaseURL     string `json:"baseUrl"`
	Is4K        bool   `json:"is4k"`
	ExternalURL string `json:"externalUrl"`
}

// URL is how Seerr reaches the server.
func (a ArrServer) URL() string {
	scheme := "http"
	if a.UseSSL {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d%s", scheme, a.Hostname, a.Port, strings.TrimRight(a.BaseURL, "/"))
}

// Radarrs lists the Radarr servers configured in Seerr (API key included,
// which requires an admin API key).
func (c *Client) Radarrs(ctx context.Context) ([]ArrServer, error) {
	var out []ArrServer
	err := c.Get(ctx, "/api/v1/settings/radarr", nil, &out)
	return out, err
}

// Sonarrs lists the Sonarr servers configured in Seerr.
func (c *Client) Sonarrs(ctx context.Context) ([]ArrServer, error) {
	var out []ArrServer
	err := c.Get(ctx, "/api/v1/settings/sonarr", nil, &out)
	return out, err
}
