package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fake answers like the Jellyfin 12.4 bench server did (testdata/ holds its
// real responses, trimmed).
func fake(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	approved := &atomic.Bool{}
	file := func(w http.ResponseWriter, name string) {
		b, err := os.ReadFile("testdata/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
	keyed := func(r *http.Request) bool { return strings.Contains(r.Header.Get("Authorization"), `Token="key"`) }
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.Method + " " + r.URL.Path {
		case "GET /System/Info/Public":
			file(w, "public")
		case "GET /QuickConnect/Enabled":
			_, _ = w.Write([]byte("true"))
		case "POST /QuickConnect/Initiate":
			file(w, "qc-initiate")
		case "GET /QuickConnect/Connect":
			if approved.Load() {
				file(w, "qc-approved")
			} else {
				file(w, "qc-pending")
			}
		case "POST /Users/AuthenticateWithQuickConnect":
			file(w, "auth-qc")
		case "POST /Users/AuthenticateByName":
			w.WriteHeader(http.StatusUnauthorized)
		case "POST /Auth/Keys":
			w.WriteHeader(http.StatusNoContent)
		case "GET /Auth/Keys":
			_, _ = w.Write([]byte(`{"Items":[{"AccessToken":"old","AppName":"Agendarr","DateCreated":"2026-01-01T00:00:00Z"},
				{"AccessToken":"key","AppName":"Agendarr","DateCreated":"2026-11-04T17:00:00Z"},{"AccessToken":"x","AppName":"Other","DateCreated":"2026-12-01T00:00:00Z"}]}`))
		case "GET /Users":
			if !keyed(r) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			file(w, "users")
		case "GET /Items":
			switch {
			case !keyed(r):
				w.WriteHeader(http.StatusUnauthorized)
			case q.Get("filters") == "IsFavorite":
				file(w, "favorites")
			case q.Get("filters") == "IsPlayed" && q.Get("includeItemTypes") == "Episode":
				file(w, "played-episodes")
			case q.Get("filters") == "IsPlayed":
				file(w, "played-movies") // the movie; the episodes' series come by id
			case q.Get("ids") == "37ed6dc273fd8a53d1a4b1b5b1e7ca9d":
				file(w, "series-by-id")
			case q.Get("ids") == "10c9ce7fbf1e264f924c0a121690285d":
				file(w, "item-movie")
			case q.Get("ids") != "":
				_, _ = w.Write([]byte(`{"Items":[]}`))
			default:
				file(w, "library")
			}
		case "GET /Shows/37ed6dc273fd8a53d1a4b1b5b1e7ca9d/Episodes":
			file(w, "season1")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, approved
}

func TestSignIn(t *testing.T) {
	ts, approved := fake(t)
	ctx := context.Background()
	c := &Client{BaseURL: ts.URL, DeviceID: "d", Version: "test"}
	info, err := c.PublicInfo(ctx)
	if err != nil || info.ID != "6d76258c55be4914b3d123ff4b13ddc5" || info.Version != "12.4.0" {
		t.Fatalf("info: %+v %v", info, err)
	}
	if on, err := c.QuickConnectEnabled(ctx); !on || err != nil {
		t.Fatalf("quick connect: %v %v", on, err)
	}
	qc, err := c.Initiate(ctx)
	if err != nil || qc.Code == "" || qc.Secret == "" {
		t.Fatalf("initiate: %+v %v", qc, err)
	}
	if ok, err := c.Approved(ctx, qc.Secret); ok || err != nil {
		t.Fatalf("pending: %v %v", ok, err)
	}
	approved.Store(true)
	if ok, err := c.Approved(ctx, qc.Secret); !ok || err != nil {
		t.Fatalf("approved: %v %v", ok, err)
	}
	a, err := c.AuthQuickConnect(ctx, qc.Secret)
	if err != nil || a.Token == "" || a.ServerID != info.ID || a.User.Name != "friend" || a.User.Policy.IsAdministrator {
		t.Fatalf("auth: %+v %v", a, err)
	}
	if _, err := c.AuthByName(ctx, "friend", "wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad password: %v", err)
	}
	if k, err := c.CreateKey(ctx, "Agendarr"); k != "key" || err != nil {
		t.Fatalf("key: %q %v", k, err)
	}
}

func TestLists(t *testing.T) {
	ts, _ := fake(t)
	ctx := context.Background()
	c := &Client{BaseURL: ts.URL, Token: "key"}
	const friend = "0534d2b624ff479cb4e2d05fded41061"
	users, err := c.Users(ctx)
	if err != nil || len(users) != 2 || users[1].Name != "friend" || users[1].Policy.IsDisabled {
		t.Fatalf("users: %+v %v", users, err)
	}
	fav, err := c.Favorites(ctx, friend)
	for _, k := range []string{"tmdb:4247", "imdb:tt0175142", "tmdbtv:232125", "tvdb:393201"} {
		if !slices.Contains(fav, k) || err != nil {
			t.Fatalf("favorites lack %s: %v %v", k, fav, err)
		}
	}
	// The two episodes were played on 2026-11-04: their series is "watching"
	// for a window that includes that day, not for one that starts after.
	day := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	if w, err := c.Watching(ctx, friend, day); !slices.Contains(w, "tvdb:393201") || err != nil {
		t.Fatalf("watching: %v %v", w, err)
	}
	if w, err := c.Watching(ctx, friend, day.AddDate(0, 0, 1)); len(w) != 0 || err != nil {
		t.Fatalf("watching later: %v %v", w, err)
	}
	if w, err := c.Watched(ctx, friend); !slices.Contains(w, "tmdb:4247") || err != nil {
		t.Fatalf("watched: %v %v", w, err)
	}
	lib, err := c.Library(ctx)
	if err != nil || lib["tmdb:4247"] != "10c9ce7fbf1e264f924c0a121690285d" || lib["tmdbtv:232125"] != "37ed6dc273fd8a53d1a4b1b5b1e7ca9d" || lib["tmdb:232125"] != "" {
		t.Fatalf("library: %v %v", lib, err)
	}
	if _, err := (&Client{BaseURL: ts.URL}).Users(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("no key: %v", err)
	}
}

func TestTracks(t *testing.T) {
	ts, _ := fake(t)
	ctx := context.Background()
	c := &Client{BaseURL: ts.URL, Token: "key"}
	vs, err := c.Tracks(ctx, "10c9ce7fbf1e264f924c0a121690285d")
	if err != nil || len(vs) != 1 {
		t.Fatalf("tracks: %+v %v", vs, err)
	}
	v := vs[0]
	if v.Resolution != "4K" || v.VideoCodec != "hevc" || v.BitDepth != 10 || !slices.Equal(v.HDR, []string{"DV", "HDR10"}) || v.Size == 0 {
		t.Fatalf("video: %+v", v)
	}
	if len(v.Audio) != 3 || v.Audio[0].Lang != "fr" || v.Audio[0].Codec != "ac3" || v.Audio[0].Layout != "5.1" || !v.Audio[0].Default || v.Audio[2].Lang != "en" {
		t.Fatalf("audio: %+v", v.Audio)
	}
	if len(v.Subs) != 2 || v.Subs[1].Title != "VOST FR" || v.Subs[1].Codec != "ass" {
		t.Fatalf("subs: %+v", v.Subs)
	}
	if gone, err := c.Tracks(ctx, "missing"); gone != nil || err != nil {
		t.Fatalf("missing: %v %v", gone, err)
	}
	eps, err := c.SeasonTracks(ctx, "37ed6dc273fd8a53d1a4b1b5b1e7ca9d", 1)
	if err != nil || len(eps) != 5 || eps[0].Season != 1 || eps[0].Episode != 1 || len(eps[0].Versions) != 1 || len(eps[0].Versions[0].Audio) != 3 {
		t.Fatalf("season: %+v %v", eps, err)
	}
}

func TestMapping(t *testing.T) {
	for in, want := range map[string]string{"DOVIWithHDR10Plus": "DV HDR10+", "DOVI": "DV", "HDR10": "HDR10", "HLG": "HLG", "SDR": "", "DOVIWithHLG": "DV HLG"} {
		if got := strings.Join(hdr(in), " "); got != want {
			t.Errorf("hdr %s: %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct{ profile, spatial, want string }{
		{"DTS-HD MA", "None", "ma"}, {"DTS:X", "None", "x"}, {"", "DolbyAtmos", "atmos"}, {"LC", "None", ""}, {"Main", "None", ""}, {"DTS-ES", "None", "es"}, {"DTS-HD HRA", "None", "hra"}, {"DTS-HD MA", "DolbyAtmos", "ma atmos"},
	} {
		if got := audioProfile(c.profile, c.spatial); got != c.want {
			t.Errorf("profile %q/%q: %q, want %q", c.profile, c.spatial, got, c.want)
		}
	}
	if got := FormatID("0534D2B624FF479CB4E2D05FDED41061"); got != "0534d2b6-24ff-479c-b4e2-d05fded41061" {
		t.Errorf("FormatID: %s", got)
	}
	if got := WebURL("https://jf.example.com/", "srv", "item"); got != "https://jf.example.com/web/index.html#!/details?id=item&serverId=srv" {
		t.Errorf("WebURL: %s", got)
	}
}
