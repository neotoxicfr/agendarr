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
			w.WriteHeader(401)
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
