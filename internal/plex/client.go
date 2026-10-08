// Package plex talks to plex.tv (sign-in, account, server resources, watchlist)
// and to a Plex Media Server (history, on deck, metadata guids).
package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Product is the X-Plex-Product header value, shown in the user's Plex
// "Authorized devices" list.
const Product = "Agendarr"

// ErrUnauthorized is returned when plex.tv rejects the token.
var ErrUnauthorized = errors.New("plex: unauthorized")

// Client calls plex.tv. The zero value is not usable; use New.
type Client struct {
	// ClientID identifies this agendarr install (X-Plex-Client-Identifier).
	// It must stay stable: plex.tv ties PINs and devices to it.
	ClientID string
	Version  string

	// Base URLs, overridable in tests.
	TVBase       string // https://plex.tv
	ClientsBase  string // https://clients.plex.tv
	DiscoverBase string // https://discover.provider.plex.tv
	AuthApp      string // https://app.plex.tv/auth

	HTTP *http.Client
}

// New returns a client for the real plex.tv.
func New(clientID, version string) *Client {
	return &Client{
		ClientID:     clientID,
		Version:      version,
		TVBase:       "https://plex.tv",
		ClientsBase:  "https://clients.plex.tv",
		DiscoverBase: "https://discover.provider.plex.tv",
		AuthApp:      "https://app.plex.tv/auth",
		HTTP:         &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) headers(h http.Header, token string) {
	h.Set("Accept", "application/json")
	h.Set("X-Plex-Product", Product)
	h.Set("X-Plex-Version", c.Version)
	h.Set("X-Plex-Client-Identifier", c.ClientID)
	h.Set("X-Plex-Platform", "Web")
	h.Set("X-Plex-Device-Name", Product)
	if token != "" {
		h.Set("X-Plex-Token", token)
	}
}

// do sends a request and decodes a JSON response into out (when non-nil).
func (c *Client) do(ctx context.Context, method, rawURL, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return err
	}
	c.headers(req.Header, token)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("plex: %s %s: %w", method, redact(rawURL), err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("plex: %s %s: HTTP %d: %s", method, redact(rawURL), res.StatusCode, body)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(out); err != nil {
		return fmt.Errorf("plex: decode %s: %w", redact(rawURL), err)
	}
	return nil
}

// redact strips the query string, which may carry a token, from logged URLs.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<url>"
	}
	u.RawQuery = ""
	return u.String()
}
