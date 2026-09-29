// Package httpx holds the small JSON-over-HTTP client shared by the Radarr,
// Sonarr and Seerr clients.
package httpx

import (
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

// ErrUnauthorized means the API key was rejected.
var ErrUnauthorized = errors.New("API key rejected")

// ErrNotAPI means something answered, but not the expected API: a wrong
// path or URL base (404), or an HTML page such as a proxy login.
var ErrNotAPI = errors.New("not the expected API")

// DefaultClient is used when none is given. The timeout is generous: a Sonarr
// calendar over four months with series included can take a few seconds on
// a NAS.
var DefaultClient = &http.Client{Timeout: 20 * time.Second}

// Client calls one instance of an *arr-style API.
type Client struct {
	BaseURL string // e.g. http://radarr:7878 or https://host/radarr
	APIKey  string
	HTTP    *http.Client
}

// Get fetches path (with query) and decodes the JSON response into out.
func (c *Client) Get(ctx context.Context, path string, q url.Values, out any) error {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("X-Api-Key", c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case res.StatusCode == http.StatusNotFound:
		return fmt.Errorf("GET %s: HTTP 404: %w", path, ErrNotAPI)
	case res.StatusCode < 200 || res.StatusCode > 299:
		body, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		return fmt.Errorf("GET %s: HTTP %d: %s", path, res.StatusCode, strings.TrimSpace(string(body)))
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		// A reverse proxy login page or the wrong port answers HTML.
		return fmt.Errorf("GET %s: got %q instead of JSON: %w", path, ct, ErrNotAPI)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(out); err != nil {
		return fmt.Errorf("GET %s: decode: %w", path, err)
	}
	return nil
}

// Fetch streams a raw response (posters). The caller closes the body.
func (c *Client) Fetch(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("X-Api-Key", c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = DefaultClient
	}
	return hc.Do(req)
}
