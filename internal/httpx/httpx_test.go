package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A redirect to another host must not carry the API key there.
func TestNoRedirect(t *testing.T) {
	var leaked bool
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("X-Api-Key") != ""
	}))
	defer b.Close()
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, b.URL+"/steal", http.StatusFound)
	}))
	defer a.Close()
	c := &Client{BaseURL: a.URL, APIKey: "secret"}
	var out any
	if err := c.Get(context.Background(), "/api", nil, &out); err == nil {
		t.Fatal("redirect answer accepted")
	}
	if leaked {
		t.Fatal("API key sent to the redirect target")
	}
}
