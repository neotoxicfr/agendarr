package plex

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Pin is a plex.tv sign-in PIN.
type Pin struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	AuthToken string `json:"authToken"`
}

// CreatePin starts a sign-in. strong=true yields a long code meant for the
// app.plex.tv/auth redirect flow (as opposed to the 4-letter link code).
func (c *Client) CreatePin(ctx context.Context) (Pin, error) {
	var p Pin
	err := c.do(ctx, http.MethodPost, c.TVBase+"/api/v2/pins?strong=true", "", &p)
	return p, err
}

// CheckPin returns the PIN; AuthToken is set once the user has signed in.
func (c *Client) CheckPin(ctx context.Context, id int64) (Pin, error) {
	var p Pin
	err := c.do(ctx, http.MethodGet, c.TVBase+"/api/v2/pins/"+strconv.FormatInt(id, 10), "", &p)
	return p, err
}

// AuthURL is the page where the user signs in to approve the PIN.
// forwardURL, when non-empty, is where Plex sends the browser afterwards.
func (c *Client) AuthURL(code, forwardURL string) string {
	q := url.Values{}
	q.Set("clientID", c.ClientID)
	q.Set("code", code)
	q.Set("context[device][product]", Product)
	if forwardURL != "" {
		q.Set("forwardUrl", forwardURL)
	}
	// Plex reads these parameters from the fragment, not the query string.
	return c.AuthApp + "#?" + q.Encode()
}

// User is a plex.tv account.
type User struct {
	ID       int64  `json:"id"`
	UUID     string `json:"uuid"`
	Username string `json:"username"`
	Title    string `json:"title"`
	Email    string `json:"email"`
	Thumb    string `json:"thumb"`
}

// DisplayName prefers the username, falling back to the title (managed
// users have no username).
func (u User) DisplayName() string {
	if u.Username != "" {
		return u.Username
	}
	return u.Title
}

// Account returns the account owning token.
func (c *Client) Account(ctx context.Context, token string) (User, error) {
	var u User
	err := c.do(ctx, http.MethodGet, c.TVBase+"/api/v2/user", token, &u)
	if err == nil && u.ID == 0 {
		err = fmt.Errorf("plex: account without id")
	}
	return u, err
}

// Connection is one way to reach a Plex Media Server.
type Connection struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	URI      string `json:"uri"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay"`
}

// Resource is a device linked to an account; servers have Provides "server".
type Resource struct {
	Name             string       `json:"name"`
	Product          string       `json:"product"`
	ClientIdentifier string       `json:"clientIdentifier"`
	Provides         string       `json:"provides"`
	Owned            bool         `json:"owned"`
	AccessToken      string       `json:"accessToken"`
	Connections      []Connection `json:"connections"`
}

// IsServer reports whether the resource is a Plex Media Server.
func (r Resource) IsServer() bool {
	for _, p := range splitComma(r.Provides) {
		if p == "server" {
			return true
		}
	}
	return false
}

// Servers returns the Plex Media Servers the account can reach, owned or
// shared with it.
func (c *Client) Servers(ctx context.Context, token string) ([]Resource, error) {
	var all []Resource
	if err := c.do(ctx, http.MethodGet, c.ClientsBase+"/api/v2/resources?includeHttps=1&includeRelay=1", token, &all); err != nil {
		return nil, err
	}
	out := all[:0]
	for _, r := range all {
		if r.IsServer() {
			out = append(out, r)
		}
	}
	return out, nil
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
