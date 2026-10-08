package server

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/neotoxicfr/agendarr/internal/config"
)

func TestBareAssets(t *testing.T) {
	h := newHarness(t, config.Config{})
	Version = "test-1"
	defer func() { Version = "dev" }()
	get := func(path string, gz bool) *http.Response {
		req, _ := http.NewRequest("GET", h.http.URL+path, nil)
		if gz {
			req.Header.Set("Accept-Encoding", "gzip")
		}
		// No transparent decompression: we want to see the encoding.
		res, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := get("/static/css/app.css?v=1", true)
	res.Body.Close()
	if res.Header.Get("Set-Cookie") != "" {
		t.Fatal("session cookie on a static asset")
	}
	res4 := get("/sw.js", false)
	sw, _ := io.ReadAll(res4.Body)
	res4.Body.Close()
	if !strings.Contains(string(sw), `const VERSION = "test-1"`) || res4.Header.Get("Set-Cookie") != "" {
		t.Fatalf("sw.js: %q", sw[:120])
	}
}
