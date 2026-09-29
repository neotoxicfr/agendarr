package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Server talks to one Plex Media Server with the owner's token (history of
// every account needs it).
type Server struct {
	BaseURL  string
	Token    string
	ClientID string
	HTTP     *http.Client
}

// OwnerAccountID is the server-side account id of the server owner; shared
// users keep their plex.tv id on the server.
const OwnerAccountID = 1

// PickConnection returns the first connection that answers /identity:
// local direct ones first, then remote direct ones, relays last.
func PickConnection(ctx context.Context, conns []Connection, token, clientID string) (string, error) {
	ordered := make([]Connection, 0, len(conns))
	for _, pass := range []func(Connection) bool{
		func(c Connection) bool { return c.Local && !c.Relay },
		func(c Connection) bool { return !c.Local && !c.Relay },
		func(c Connection) bool { return c.Relay },
	} {
		for _, c := range conns {
			if pass(c) {
				ordered = append(ordered, c)
			}
		}
	}
	var errs []error
	for _, c := range ordered {
		s := &Server{BaseURL: c.URI, Token: token, ClientID: clientID}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.Identity(cctx)
		cancel()
		if err == nil {
			return c.URI, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", redact(c.URI), err))
	}
	if len(errs) == 0 {
		return "", errors.New("plex: the server advertises no connection")
	}
	return "", errors.Join(errs...)
}

func (s *Server) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return defaultServerClient
}

// TLS is verified as usual: the connections plex.tv advertises use
// *.plex.direct names with valid certificates, and a URL set by the admin
// inside a Docker network is typically plain http://plex:32400. Relaxing the
// check would expose the owner's token to anyone on the LAN.
var defaultServerClient = &http.Client{Timeout: 30 * time.Second}

func (s *Server) get(ctx context.Context, path string, q url.Values, out any) error {
	u := strings.TrimRight(s.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", s.Token)
	req.Header.Set("X-Plex-Client-Identifier", s.ClientID)
	req.Header.Set("X-Plex-Product", Product)
	res, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("plex server: %s: %w", path, err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case res.StatusCode == http.StatusNotFound:
		return errNotFound
	case res.StatusCode < 200 || res.StatusCode > 299:
		return fmt.Errorf("plex server: %s: HTTP %d", path, res.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(out)
}

var errNotFound = errors.New("plex server: not found")

// Identity checks that the server answers with this token.
func (s *Server) Identity(ctx context.Context) error {
	var v struct {
		MediaContainer struct {
			MachineIdentifier string `json:"machineIdentifier"`
		} `json:"MediaContainer"`
	}
	if err := s.get(ctx, "/identity", nil, &v); err != nil {
		return err
	}
	if v.MediaContainer.MachineIdentifier == "" {
		return errors.New("plex server: /identity without machineIdentifier")
	}
	return nil
}

// Viewed is one history entry reduced to what the filters need: the
// ratingKey of the movie, or of the show for an episode.
type Viewed struct {
	Type      string // movie | episode
	RatingKey string // movie or show
	ViewedAt  time.Time
}

// History returns what accountID watched since since (zero: everything),
// newest first, capped at limit entries.
func (s *Server) History(ctx context.Context, accountID int64, since time.Time, limit int) ([]Viewed, error) {
	const page = 500
	var out []Viewed
	for start := 0; start < limit; start += page {
		q := url.Values{
			"sort":                   {"viewedAt:desc"},
			"accountID":              {strconv.FormatInt(accountID, 10)},
			"X-Plex-Container-Start": {strconv.Itoa(start)},
			"X-Plex-Container-Size":  {strconv.Itoa(page)},
		}
		if !since.IsZero() {
			q.Set("viewedAt>", strconv.FormatInt(since.Unix(), 10))
		}
		var v struct {
			MediaContainer struct {
				TotalSize int `json:"totalSize"`
				Metadata  []struct {
					Type                 string `json:"type"`
					RatingKey            string `json:"ratingKey"`
					GrandparentRatingKey string `json:"grandparentRatingKey"`
					GrandparentKey       string `json:"grandparentKey"`
					ViewedAt             int64  `json:"viewedAt"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := s.get(ctx, "/status/sessions/history/all", q, &v); err != nil {
			return nil, err
		}
		for _, m := range v.MediaContainer.Metadata {
			rk := m.RatingKey
			if m.Type == "episode" {
				// History entries carry the show as grandparentKey
				// (/library/metadata/2199); grandparentRatingKey is
				// often absent.
				rk = m.GrandparentRatingKey
				if rk == "" {
					rk = strings.TrimPrefix(m.GrandparentKey, "/library/metadata/")
				}
			}
			if rk == "" || (m.Type != "movie" && m.Type != "episode") {
				continue
			}
			out = append(out, Viewed{Type: m.Type, RatingKey: rk, ViewedAt: time.Unix(m.ViewedAt, 0)})
		}
		if len(v.MediaContainer.Metadata) < page || start+page >= v.MediaContainer.TotalSize {
			break
		}
	}
	return out, nil
}

// Guids resolves ratingKeys to external ids ("tmdb:1", "tvdb:2",
// "imdb:tt3"), in batches. Deleted items are simply absent from the result.
func (s *Server) Guids(ctx context.Context, keys []string) (map[string][]string, error) {
	out := make(map[string][]string, len(keys))
	const batch = 100
	for i := 0; i < len(keys); i += batch {
		j := min(i+batch, len(keys))
		var v struct {
			MediaContainer struct {
				Metadata []struct {
					RatingKey string `json:"ratingKey"`
					Guid      []struct {
						ID string `json:"id"`
					} `json:"Guid"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		err := s.get(ctx, "/library/metadata/"+strings.Join(keys[i:j], ","), nil, &v)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		for _, m := range v.MediaContainer.Metadata {
			var ids []string
			for _, g := range m.Guid {
				if k := GuidKey(g.ID); k != "" {
					ids = append(ids, k)
				}
			}
			out[m.RatingKey] = ids
		}
	}
	return out, nil
}

// GuidKey turns "tmdb://123" into "tmdb:123"; other agents give "".
func GuidKey(guid string) string {
	for _, p := range []string{"tmdb", "tvdb", "imdb"} {
		if v, ok := strings.CutPrefix(guid, p+"://"); ok && v != "" {
			return p + ":" + v
		}
	}
	return ""
}

// Watchlist returns the external ids on the account's Plex watchlist.
func (c *Client) Watchlist(ctx context.Context, token string) ([]string, error) {
	var out []string
	const page = 100
	for start := 0; start < 5000; start += page {
		var v struct {
			MediaContainer struct {
				TotalSize int `json:"totalSize"`
				Metadata  []struct {
					Guid []struct {
						ID string `json:"id"`
					} `json:"Guid"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		u := fmt.Sprintf("%s/library/sections/watchlist/all?includeGuids=1&X-Plex-Container-Start=%d&X-Plex-Container-Size=%d",
			c.DiscoverBase, start, page)
		if err := c.do(ctx, http.MethodGet, u, token, &v); err != nil {
			return nil, err
		}
		for _, m := range v.MediaContainer.Metadata {
			for _, g := range m.Guid {
				if k := GuidKey(g.ID); k != "" {
					out = append(out, k)
				}
			}
		}
		if len(v.MediaContainer.Metadata) < page || start+page >= v.MediaContainer.TotalSize {
			break
		}
	}
	return out, nil
}
