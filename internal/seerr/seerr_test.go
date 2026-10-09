package seerr

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestSeerr(t *testing.T) {
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, s string) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(s))
	}
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) { j(w, `{"version":"3.0.0"}`) })
	mux.HandleFunc("GET /api/v1/auth/me", func(w http.ResponseWriter, r *http.Request) { j(w, `{"id":1}`) })
	mux.HandleFunc("GET /api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		// Two pages of one user each.
		if r.URL.Query().Get("skip") == "0" {
			j(w, `{"pageInfo":{"results":2},"results":[{"id":1,"plexId":7,"plexUsername":"neo"}]}`)
			return
		}
		j(w, `{"pageInfo":{"results":2},"results":[{"id":2,"plexId":0,"plexUsername":"Friend","email":"f@x"}]}`)
	})
	mux.HandleFunc("GET /api/v1/request", func(w http.ResponseWriter, r *http.Request) {
		// An old server ignoring requestedBy returns everyone's requests.
		skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		if skip > 0 {
			j(w, `{"pageInfo":{"results":3},"results":[{"id":3,"type":"tv","requestedBy":{"id":2},"media":{"tmdbId":5,"tvdbId":6}}]}`)
			return
		}
		j(w, `{"pageInfo":{"results":3},"results":[
		  {"id":1,"type":"movie","requestedBy":{"id":2},"media":{"tmdbId":42}},
		  {"id":2,"type":"movie","requestedBy":{"id":1},"media":{"tmdbId":43}}]}`)
	})
	mux.HandleFunc("GET /api/v1/settings/radarr", func(w http.ResponseWriter, r *http.Request) {
		j(w, `[{"id":0,"name":"Radarr 4K","hostname":"radarr4k","port":7879,"apiKey":"rk","useSsl":false,"baseUrl":"/r/","is4k":true}]`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ctx := context.Background()
	c := New(ts.URL, "k")

	if s, err := c.Test(ctx); err != nil || s.Version != "3.0.0" {
		t.Fatalf("test: %v", err)
	}
	users, err := c.Users(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("users: %+v %v", users, err)
	}
	reqs, err := c.AllRequests(ctx)
	if err != nil || len(reqs) != 3 {
		t.Fatalf("requests: %+v %v", reqs, err)
	}
	if got := fmt.Sprint(reqs[2].MediaKeys()); got != "[tmdbtv:5 tvdb:6]" { // a tv request: SEC-05
		t.Fatalf("keys %s", got)
	}
	rs, err := c.Radarrs(ctx)
	if err != nil || len(rs) != 1 || rs[0].URL() != "http://radarr4k:7879/r" || !rs[0].Is4K {
		t.Fatalf("radarrs: %+v %v", rs, err)
	}
}
