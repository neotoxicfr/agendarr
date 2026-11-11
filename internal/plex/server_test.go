package plex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerHistoryAndGuids(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /identity", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"MediaContainer":{"machineIdentifier":"A"}}`)
	})
	mux.HandleFunc("GET /status/sessions/history/all", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "tok" || r.URL.Query().Get("accountID") != "42" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Shape captured from PMS: episodes carry the show as grandparentKey only.
		io.WriteString(w, `{"MediaContainer":{"totalSize":3,"Metadata":[
		 {"type":"episode","ratingKey":"232949","grandparentKey":"/library/metadata/2199","viewedAt":1788517790},
		 {"type":"movie","ratingKey":"83745","viewedAt":1788456147},
		 {"type":"track","ratingKey":"1","viewedAt":1}]}}`)
	})
	mux.HandleFunc("GET /library/metadata/{ids}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("ids") != "2199,83745,999" || r.URL.Query().Get("includeGuids") != "1" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"MediaContainer":{"Metadata":[
		 {"ratingKey":"2199","guid":"plex://show/x","Guid":[{"id":"imdb://tt0149460"},{"id":"tmdb://615"},{"id":"tvdb://73871"}]},
		 {"ratingKey":"83745","Guid":[{"id":"tmdb://616037"},{"id":"plex://movie/x"}]}]}}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ctx := context.Background()

	uri, err := PickConnection(ctx, []Connection{
		{URI: "http://127.0.0.1:1", Relay: true},
		{URI: "http://127.0.0.1:2", Local: false},
		{URI: ts.URL, Local: true},
	}, "tok", "cid")
	if err != nil || uri != ts.URL {
		t.Fatalf("pick: %q %v", uri, err)
	}
	s := &Server{BaseURL: uri, Token: "tok", ClientID: "cid"}
	h, err := s.History(ctx, 42, time.Time{}, 1000)
	if err != nil || len(h) != 2 || h[0].RatingKey != "2199" || h[1].Type != "movie" {
		t.Fatalf("history: %+v %v", h, err)
	}
	g, err := s.Guids(ctx, []string{"2199", "83745", "999"})
	if err != nil || strings.Join(g["2199"], ",") != "tvdb:73871,tmdb:615,imdb:tt0149460" && len(g["2199"]) != 3 || len(g["83745"]) != 1 {
		t.Fatalf("guids: %v %v", g, err)
	}
	if _, err := PickConnection(ctx, []Connection{{URI: "http://127.0.0.1:1"}}, "tok", "cid"); err == nil {
		t.Fatal("dead connection picked")
	}
}

func TestHDROf(t *testing.T) {
	for _, c := range []struct {
		dovi         bool
		trc, display string
		want         string
	}{
		{true, "smpte2084", "4K DoVi/HDR10+", "DV HDR10+"},
		{true, "smpte2084", "4K DoVi/HDR10", "DV HDR10"},
		{false, "smpte2084", "4K HDR10", "HDR10"},
		{false, "arib-std-b67", "4K HLG", "HLG"},
		{false, "bt709", "1080p", ""},
	} {
		if got := strings.Join(hdrOf(c.dovi, c.trc, c.display), " "); got != c.want {
			t.Fatalf("%+v: %q", c, got)
		}
	}
}

// Without a valid account Plex would answer with every account's history:
// the call is refused before any request.
func TestHistoryNeedsAccount(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer ts.Close()
	s := &Server{BaseURL: ts.URL, HTTP: ts.Client()}
	for _, id := range []int64{0, -1} {
		if _, err := s.History(context.Background(), id, time.Time{}, 10); err == nil {
			t.Fatalf("account %d: no error", id)
		}
	}
	if hits != 0 {
		t.Fatalf("%d requests sent", hits)
	}
}

// A movie's TVDB id is not a series' (EDGE-a1-01): only shows keep a tvdb key.
func TestLibraryAndWatchlistDropMovieTvdb(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /library/sections", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"MediaContainer":{"Directory":[{"key":"1","type":"movie"},{"key":"2","type":"show"}]}}`)
	})
	mux.HandleFunc("GET /library/sections/1/all", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"9","Guid":[{"id":"tmdb://1"},{"id":"tvdb://73871"}]}]}}`)
	})
	mux.HandleFunc("GET /library/sections/2/all", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"8","Guid":[{"id":"tvdb://5"}]}]}}`)
	})
	mux.HandleFunc("GET /library/sections/watchlist/all", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"MediaContainer":{"totalSize":2,"Metadata":[
		 {"type":"movie","Guid":[{"id":"tmdb://1"},{"id":"tvdb://73871"}]},
		 {"type":"show","Guid":[{"id":"tvdb://5"}]}]}}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ctx := context.Background()
	lib, err := (&Server{BaseURL: ts.URL, Token: "t"}).Library(ctx)
	if err != nil || lib["tvdb:73871"] != "" || lib["tmdb:1"] != "9" || lib["tvdb:5"] != "8" {
		t.Fatalf("library: %v %v", lib, err)
	}
	c := New("cid", "test")
	c.DiscoverBase = ts.URL
	wl, err := c.Watchlist(ctx, "t")
	if err != nil || strings.Join(wl, ",") != "tmdb:1,tvdb:5" {
		t.Fatalf("watchlist: %v %v", wl, err)
	}
}

func TestResolution(t *testing.T) {
	for in, want := range map[string]string{"2k": "1440p", "4k": "4K", "8k": "8K", "1080": "1080p", "720p": "720p", "sd": "SD", "": ""} {
		if got := resolution(in); got != want {
			t.Errorf("resolution(%q) = %q, want %q", in, got, want)
		}
	}
}

// An episode Plex lacks is "absent", not an error to retry (EDGE-a1-03).
func TestEpisodeKeyAbsent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"301","parentIndex":1,"index":2}]}}`)
	}))
	defer ts.Close()
	s := &Server{BaseURL: ts.URL, Token: "t"}
	if rk, err := s.EpisodeKey(context.Background(), "1", 1, 9); rk != "" || err != nil {
		t.Fatalf("absent: %q %v", rk, err)
	}
	if rk, err := s.EpisodeKey(context.Background(), "1", 1, 2); rk != "301" || err != nil {
		t.Fatalf("present: %q %v", rk, err)
	}
}
