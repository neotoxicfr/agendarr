// Package jellyfin talks to a Jellyfin server: sign-in (Quick Connect or
// password), the server's users, each user's favorites and history read with
// the API key, the library index, and the tracks of a file. It mirrors what
// the plex package offers the rest of the app, in the same shapes.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls one Jellyfin server. Token is the server's API key, or a
// user's token for the calls a sign-in makes; empty before sign-in.
type Client struct {
	BaseURL  string
	Token    string
	DeviceID string // one per sign-in attempt: Jellyfin keeps a session per device
	Version  string
	HTTP     *http.Client
}

var (
	// ErrUnauthorized is returned when Jellyfin rejects the credentials or token.
	ErrUnauthorized = errors.New("jellyfin: unauthorized")
	// ErrNotFound is returned for a missing item, or an expired Quick Connect secret.
	ErrNotFound = errors.New("jellyfin: not found")
)

// Redirects are not followed, so the API key cannot be carried to another
// host; TLS is verified as usual.
var defaultClient = &http.Client{Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	auth := fmt.Sprintf(`MediaBrowser Client="Agendarr", Device="Agendarr", DeviceId=%q, Version=%q`, c.DeviceID, c.Version)
	if c.Token != "" {
		auth += fmt.Sprintf(`, Token=%q`, c.Token)
	}
	req.Header.Set("Authorization", auth)
	hc := c.HTTP
	if hc == nil {
		hc = defaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("jellyfin: %s: %w", path, err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case res.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case res.StatusCode < 200 || res.StatusCode > 299:
		return fmt.Errorf("jellyfin: %s: HTTP %d", path, res.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(out)
}

// Info is what the server says about itself without credentials.
type Info struct {
	ID      string `json:"Id"`
	Name    string `json:"ServerName"`
	Version string `json:"Version"`
}

// PublicInfo identifies the server.
func (c *Client) PublicInfo(ctx context.Context) (Info, error) {
	var v Info
	err := c.do(ctx, http.MethodGet, "/System/Info/Public", nil, nil, &v)
	if err == nil && v.ID == "" {
		err = errors.New("jellyfin: not a Jellyfin server")
	}
	return v, err
}

// User is a Jellyfin account.
type User struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Policy struct {
		IsAdministrator bool `json:"IsAdministrator"`
		IsDisabled      bool `json:"IsDisabled"`
	} `json:"Policy"`
}

// Auth is a successful sign-in.
type Auth struct {
	Token    string `json:"AccessToken"`
	ServerID string `json:"ServerId"`
	User     User   `json:"User"`
}

// QuickConnectEnabled reports whether the server offers Quick Connect.
func (c *Client) QuickConnectEnabled(ctx context.Context) (bool, error) {
	var on bool
	err := c.do(ctx, http.MethodGet, "/QuickConnect/Enabled", nil, nil, &on)
	return on, err
}

// QuickConnect is a pending request: Code is shown to the person, Secret
// stays on the server side.
type QuickConnect struct {
	Code          string `json:"Code"`
	Secret        string `json:"Secret"`
	Authenticated bool   `json:"Authenticated"`
}

// Initiate starts a Quick Connect request.
func (c *Client) Initiate(ctx context.Context) (QuickConnect, error) {
	var v QuickConnect
	err := c.do(ctx, http.MethodPost, "/QuickConnect/Initiate", nil, nil, &v)
	return v, err
}

// Approved reports whether the person approved the code in a Jellyfin app.
func (c *Client) Approved(ctx context.Context, secret string) (bool, error) {
	var v QuickConnect
	err := c.do(ctx, http.MethodGet, "/QuickConnect/Connect", url.Values{"secret": {secret}}, nil, &v)
	return v.Authenticated, err
}

// AuthQuickConnect signs in with an approved Quick Connect secret.
func (c *Client) AuthQuickConnect(ctx context.Context, secret string) (Auth, error) {
	var v Auth
	err := c.do(ctx, http.MethodPost, "/Users/AuthenticateWithQuickConnect", nil, map[string]string{"Secret": secret}, &v)
	return v, err
}

// AuthByName signs in with a password, which is only passed on.
func (c *Client) AuthByName(ctx context.Context, name, password string) (Auth, error) {
	var v Auth
	err := c.do(ctx, http.MethodPost, "/Users/AuthenticateByName", nil, map[string]string{"Username": name, "Pw": password}, &v)
	return v, err
}

// Logout ends the session of the client's token, so a sign-in leaves no
// device behind on the server.
func (c *Client) Logout(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/Sessions/Logout", nil, nil, nil)
}

// CreateKey makes an API key for app (an administrator's token is needed) and
// returns it: Jellyfin answers the creation with no body, so the key is read
// back from the list, the newest one with that name.
func (c *Client) CreateKey(ctx context.Context, app string) (string, error) {
	if err := c.do(ctx, http.MethodPost, "/Auth/Keys", url.Values{"app": {app}}, nil, nil); err != nil {
		return "", err
	}
	var v struct {
		Items []struct {
			AccessToken string    `json:"AccessToken"`
			AppName     string    `json:"AppName"`
			DateCreated time.Time `json:"DateCreated"`
		} `json:"Items"`
	}
	if err := c.do(ctx, http.MethodGet, "/Auth/Keys", nil, nil, &v); err != nil {
		return "", err
	}
	var key string
	var at time.Time
	for _, k := range v.Items {
		if k.AppName == app && !k.DateCreated.Before(at) {
			key, at = k.AccessToken, k.DateCreated
		}
	}
	if key == "" {
		return "", errors.New("jellyfin: API key not found after creation")
	}
	return key, nil
}

// Users lists the server's accounts (API key).
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var v []User
	err := c.do(ctx, http.MethodGet, "/Users", nil, nil, &v)
	return v, err
}

// item is a library item, with what the app reads from it.
type item struct {
	ID          string            `json:"Id"`
	Type        string            `json:"Type"` // Movie, Series, Episode
	SeriesID    string            `json:"SeriesId"`
	Season      int               `json:"ParentIndexNumber"`
	Episode     int               `json:"IndexNumber"`
	ProviderIDs map[string]string `json:"ProviderIds"`
	UserData    struct {
		LastPlayedDate time.Time `json:"LastPlayedDate"`
	} `json:"UserData"`
	MediaSources []mediaSource `json:"MediaSources"`
}

// page is how many items one list call asks for.
const page = 500

// items pages through /Items with q, up to limit items.
func (c *Client) items(ctx context.Context, q url.Values, limit int, each func(item) bool) error {
	for start := 0; start < limit; start += page {
		q.Set("startIndex", fmt.Sprint(start))
		q.Set("limit", fmt.Sprint(min(page, limit-start)))
		var v struct {
			Items []item `json:"Items"`
		}
		if err := c.do(ctx, http.MethodGet, "/Items", q, nil, &v); err != nil {
			return err
		}
		for _, it := range v.Items {
			if !each(it) {
				return nil
			}
		}
		if len(v.Items) < page {
			return nil
		}
	}
	return nil
}

// Keys turns an item's provider ids into the app's keys ("tmdb:1",
// "tmdbtv:1" for a series, "tvdb:2", "imdb:tt3").
func Keys(ids map[string]string, series bool) []string {
	var out []string
	for _, p := range []struct{ name, prefix string }{{"Tmdb", "tmdb:"}, {"Tvdb", "tvdb:"}, {"Imdb", "imdb:"}} {
		v := ids[p.name]
		if v == "" {
			continue
		}
		k := p.prefix + v
		if series && p.prefix == "tmdb:" {
			k = "tmdbtv:" + v // TMDB numbers movies and shows separately
		}
		out = append(out, k)
	}
	return out
}

// Favorites returns the keys of the movies and series the user marked as
// favorites, Jellyfin's closest thing to a watchlist.
func (c *Client) Favorites(ctx context.Context, userID string) ([]string, error) {
	var out []string
	err := c.items(ctx, url.Values{"userId": {userID}, "recursive": {"true"}, "filters": {"IsFavorite"},
		"includeItemTypes": {"Movie,Series"}, "fields": {"ProviderIds"}}, 5000, func(it item) bool {
		out = append(out, Keys(it.ProviderIDs, it.Type == "Series")...)
		return true
	})
	return out, err
}

// Watching returns the keys of the series the user played an episode of
// since since, most recent first.
func (c *Client) Watching(ctx context.Context, userID string, since time.Time) ([]string, error) {
	var series []string
	seen := map[string]bool{}
	err := c.items(ctx, url.Values{"userId": {userID}, "recursive": {"true"}, "filters": {"IsPlayed"},
		"includeItemTypes": {"Episode"}, "sortBy": {"DatePlayed"}, "sortOrder": {"Descending"}}, 5000, func(it item) bool {
		if it.UserData.LastPlayedDate.Before(since) {
			return false
		}
		if it.SeriesID != "" && !seen[it.SeriesID] {
			seen[it.SeriesID] = true
			series = append(series, it.SeriesID)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return c.seriesKeys(ctx, series)
}

// Watched returns the keys of every movie the user played and every series
// they played an episode of.
func (c *Client) Watched(ctx context.Context, userID string) ([]string, error) {
	var out, series []string
	seen := map[string]bool{}
	err := c.items(ctx, url.Values{"userId": {userID}, "recursive": {"true"}, "filters": {"IsPlayed"},
		"includeItemTypes": {"Movie,Episode"}, "fields": {"ProviderIds"}}, 50000, func(it item) bool {
		switch {
		case it.Type == "Movie":
			out = append(out, Keys(it.ProviderIDs, false)...)
		case it.SeriesID != "" && !seen[it.SeriesID]:
			seen[it.SeriesID] = true
			series = append(series, it.SeriesID)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	keys, err := c.seriesKeys(ctx, series)
	return append(out, keys...), err
}

// seriesKeys reads the provider ids of series by item id, 100 at a time.
func (c *Client) seriesKeys(ctx context.Context, ids []string) ([]string, error) {
	var out []string
	for len(ids) > 0 {
		n := min(100, len(ids))
		err := c.items(ctx, url.Values{"ids": {strings.Join(ids[:n], ",")}, "fields": {"ProviderIds"}}, n, func(it item) bool {
			out = append(out, Keys(it.ProviderIDs, true)...)
			return true
		})
		if err != nil {
			return nil, err
		}
		ids = ids[n:]
	}
	return out, nil
}

// Library maps each key of the server's movies and series to its item id.
// A series and a movie sharing a TMDB number do not collide: series get
// "tmdbtv:" keys.
func (c *Client) Library(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := c.items(ctx, url.Values{"recursive": {"true"}, "includeItemTypes": {"Movie,Series"}, "fields": {"ProviderIds"}}, 200000,
		func(it item) bool {
			for _, k := range Keys(it.ProviderIDs, it.Type == "Series") {
				if _, dup := out[k]; !dup {
					out[k] = it.ID
				}
			}
			return true
		})
	return out, err
}

// WebURL opens an item in Jellyfin's web client.
func WebURL(base, serverID, itemID string) string {
	return strings.TrimRight(base, "/") + "/web/index.html#!/details?id=" + url.QueryEscape(itemID) +
		"&serverId=" + url.QueryEscape(serverID)
}

// FormatID writes a Jellyfin id as a dashed GUID, the form agendarr stores
// (a dashed id is never read as a number by SQLite).
func FormatID(id string) string {
	id = strings.ToLower(strings.ReplaceAll(id, "-", ""))
	if len(id) != 32 {
		return id
	}
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}
