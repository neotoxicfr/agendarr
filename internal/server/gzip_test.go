package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/neotoxicfr/agendarr/internal/config"
)

func TestGzipAndBareAssets(t *testing.T) {
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
	defer res.Body.Close()
	if res.Header.Get("Content-Encoding") != "gzip" || res.Header.Get("Set-Cookie") != "" {
		t.Fatalf("css: enc=%q cookie=%q", res.Header.Get("Content-Encoding"), res.Header.Get("Set-Cookie"))
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	css, _ := io.ReadAll(zr)
	if !strings.Contains(string(css), ":root") {
		t.Fatal("css not decodable")
	}
	res2 := get("/login", false)
	res2.Body.Close()
	if res2.Header.Get("Content-Encoding") != "" {
		t.Fatal("compressed without Accept-Encoding")
	}
	res3 := get("/static/icons/icon-192.png", true)
	res3.Body.Close()
	if res3.Header.Get("Content-Encoding") != "" {
		t.Fatal("PNG compressed")
	}
	res4 := get("/sw.js", false)
	sw, _ := io.ReadAll(res4.Body)
	res4.Body.Close()
	if !strings.Contains(string(sw), `const VERSION = "test-1"`) || res4.Header.Get("Set-Cookie") != "" {
		t.Fatalf("sw.js: %q", sw[:120])
	}
}
