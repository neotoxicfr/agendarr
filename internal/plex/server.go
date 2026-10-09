package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Server talks to one Plex Media Server with the owner's token (history of
// every account needs it).
type Server struct {
	BaseURL  string
	Token    string
	ClientID string
	HTTP     *http.Client
}

// OwnerAccountID is the server-side account id of the server owner; shared
// users keep their plex.tv id on the server.
const OwnerAccountID = 1

// PickConnection returns the first connection that answers /identity:
// local direct ones first, then remote direct ones, relays last.
func PickConnection(ctx context.Context, conns []Connection, token, clientID string) (string, error) {
	ordered := make([]Connection, 0, len(conns))
	for _, pass := range []func(Connection) bool{
		func(c Connection) bool { return c.Local && !c.Relay },
		func(c Connection) bool { return !c.Local && !c.Relay },
		func(c Connection) bool { return c.Relay },
	} {
		for _, c := range conns {
			if pass(c) {
				ordered = append(ordered, c)
			}
		}
	}
	var errs []error
	for _, c := range ordered {
		s := &Server{BaseURL: c.URI, Token: token, ClientID: clientID}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.Identity(cctx)
		cancel()
		if err == nil {
			return c.URI, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", redact(c.URI), err))
	}
	if len(errs) == 0 {
		return "", errors.New("plex: the server advertises no connection")
	}
	return "", errors.Join(errs...)
}

func (s *Server) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return defaultServerClient
}

// TLS is verified as usual: the connections plex.tv advertises use
// *.plex.direct names with valid certificates, and a URL set by the admin
// inside a Docker network is typically plain http://plex:32400. Relaxing the
// check would expose the owner's token to anyone on the LAN.
// Redirects are not followed, so the owner's X-Plex-Token cannot be carried
// to another host.
var defaultServerClient = &http.Client{Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (s *Server) get(ctx context.Context, path string, q url.Values, out any) error {
	u := strings.TrimRight(s.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", s.Token)
	req.Header.Set("X-Plex-Client-Identifier", s.ClientID)
	req.Header.Set("X-Plex-Product", Product)
	res, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("plex server: %s: %w", path, err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case res.StatusCode == http.StatusNotFound:
		return errNotFound
	case res.StatusCode < 200 || res.StatusCode > 299:
		return fmt.Errorf("plex server: %s: HTTP %d", path, res.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(out)
}

var errNotFound = errors.New("plex server: not found")

// Identity checks that the server answers with this token.
func (s *Server) Identity(ctx context.Context) error {
	var v struct {
		MediaContainer struct {
			MachineIdentifier string `json:"machineIdentifier"`
		} `json:"MediaContainer"`
	}
	if err := s.get(ctx, "/identity", nil, &v); err != nil {
		return err
	}
	if v.MediaContainer.MachineIdentifier == "" {
		return errors.New("plex server: /identity without machineIdentifier")
	}
	return nil
}

// Viewed is one history entry reduced to what the filters need: the
// ratingKey of the movie, or of the show for an episode.
type Viewed struct {
	Type      string // movie | episode
	RatingKey string // movie or show
	ViewedAt  time.Time
}

// History returns what accountID watched since since (zero: everything),
// newest first, capped at limit entries.
func (s *Server) History(ctx context.Context, accountID int64, since time.Time, limit int) ([]Viewed, error) {
	const page = 500
	var out []Viewed
	for start := 0; start < limit; start += page {
		q := url.Values{
			"sort":                   {"viewedAt:desc"},
			"accountID":              {strconv.FormatInt(accountID, 10)},
			"X-Plex-Container-Start": {strconv.Itoa(start)},
			"X-Plex-Container-Size":  {strconv.Itoa(page)},
		}
		if !since.IsZero() {
			q.Set("viewedAt>", strconv.FormatInt(since.Unix(), 10))
		}
		var v struct {
			MediaContainer struct {
				TotalSize int `json:"totalSize"`
				Metadata  []struct {
					Type                 string `json:"type"`
					RatingKey            string `json:"ratingKey"`
					GrandparentRatingKey string `json:"grandparentRatingKey"`
					GrandparentKey       string `json:"grandparentKey"`
					ViewedAt             int64  `json:"viewedAt"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := s.get(ctx, "/status/sessions/history/all", q, &v); err != nil {
			return nil, err
		}
		for _, m := range v.MediaContainer.Metadata {
			rk := m.RatingKey
			if m.Type == "episode" {
				// History entries carry the show as grandparentKey
				// (/library/metadata/2199); grandparentRatingKey is
				// often absent.
				rk = m.GrandparentRatingKey
				if rk == "" {
					rk = strings.TrimPrefix(m.GrandparentKey, "/library/metadata/")
				}
			}
			if rk == "" || (m.Type != "movie" && m.Type != "episode") {
				continue
			}
			out = append(out, Viewed{Type: m.Type, RatingKey: rk, ViewedAt: time.Unix(m.ViewedAt, 0)})
		}
		if len(v.MediaContainer.Metadata) < page || start+page >= v.MediaContainer.TotalSize {
			break
		}
	}
	return out, nil
}

// Guids resolves ratingKeys to external ids ("tmdb:1", "tvdb:2",
// "imdb:tt3"), in batches. Deleted items are simply absent from the result.
func (s *Server) Guids(ctx context.Context, keys []string) (map[string][]string, error) {
	out := make(map[string][]string, len(keys))
	const batch = 100
	for i := 0; i < len(keys); i += batch {
		j := min(i+batch, len(keys))
		var v struct {
			MediaContainer struct {
				Metadata []struct {
					RatingKey string `json:"ratingKey"`
					// "guid" is the plex:// string; without this exact
					// field encoding/json would fold it into "Guid".
					PlexGuid string `json:"guid"`
					Guid     []struct {
						ID string `json:"id"`
					} `json:"Guid"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		err := s.get(ctx, "/library/metadata/"+strings.Join(keys[i:j], ","), url.Values{"includeGuids": {"1"}}, &v)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		for _, m := range v.MediaContainer.Metadata {
			var ids []string
			for _, g := range m.Guid {
				if k := GuidKey(g.ID); k != "" {
					ids = append(ids, k)
				}
			}
			out[m.RatingKey] = ids
		}
	}
	return out, nil
}

// GuidKey turns "tmdb://123" into "tmdb:123"; other agents give "".
func GuidKey(guid string) string {
	for _, p := range []string{"tmdb", "tvdb", "imdb"} {
		if v, ok := strings.CutPrefix(guid, p+"://"); ok && v != "" {
			return p + ":" + v
		}
	}
	return ""
}

// Watchlist returns the external ids on the account's Plex watchlist.
func (c *Client) Watchlist(ctx context.Context, token string) ([]string, error) {
	var out []string
	const page = 100
	for start := 0; start < 5000; start += page {
		var v struct {
			MediaContainer struct {
				TotalSize int `json:"totalSize"`
				Metadata  []struct {
					PlexGuid string `json:"guid"` // see Guids
					Guid     []struct {
						ID string `json:"id"`
					} `json:"Guid"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		u := fmt.Sprintf("%s/library/sections/watchlist/all?includeGuids=1&X-Plex-Container-Start=%d&X-Plex-Container-Size=%d",
			c.DiscoverBase, start, page)
		if err := c.do(ctx, http.MethodGet, u, token, &v); err != nil {
			return nil, err
		}
		for _, m := range v.MediaContainer.Metadata {
			for _, g := range m.Guid {
				if k := GuidKey(g.ID); k != "" {
					out = append(out, k)
				}
			}
		}
		if len(v.MediaContainer.Metadata) < page || start+page >= v.MediaContainer.TotalSize {
			break
		}
	}
	return out, nil
}

// Library maps the external ids ("tmdb:1", "tvdb:2", "imdb:tt3") of every
// movie and show on the server to its ratingKey.
func (s *Server) Library(ctx context.Context) (map[string]string, error) {
	var secs struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := s.get(ctx, "/library/sections", nil, &secs); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, d := range secs.MediaContainer.Directory {
		if d.Type != "movie" && d.Type != "show" {
			continue
		}
		var v struct {
			MediaContainer struct {
				Metadata []struct {
					RatingKey string `json:"ratingKey"`
					PlexGuid  string `json:"guid"` // see Guids
					Guid      []struct {
						ID string `json:"id"`
					} `json:"Guid"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := s.get(ctx, "/library/sections/"+url.PathEscape(d.Key)+"/all", url.Values{"includeGuids": {"1"}}, &v); err != nil {
			return nil, err
		}
		for _, m := range v.MediaContainer.Metadata {
			for _, g := range m.Guid {
				if k := GuidKey(g.ID); k != "" {
					out[k] = m.RatingKey
				}
			}
		}
	}
	return out, nil
}

// WebURL opens a library item in Plex Web for anyone the server is shared with.
func WebURL(machineID, ratingKey string) string {
	return "https://app.plex.tv/desktop/#!/server/" + url.PathEscape(machineID) +
		"/details?key=" + url.QueryEscape("/library/metadata/"+ratingKey)
}

// Version is one file of a library item, as Plex analysed it: video
// format, size, and its audio and subtitle tracks.
type Version struct {
	Resolution string   `json:"resolution,omitempty"` // 4K, 1080p
	VideoCodec string   `json:"videoCodec,omitempty"` // hevc
	BitDepth   int      `json:"bitDepth,omitempty"`
	HDR        []string `json:"hdr,omitempty"` // DV, HDR10+, HDR10, HLG
	Size       int64    `json:"size,omitempty"`
	Audio      []Track  `json:"audio"`
	Subs       []Track  `json:"subs"`
}

// Track is one audio or subtitle stream.
type Track struct {
	Codec    string `json:"codec,omitempty"`   // aac, eac3, srt, pgs…
	Profile  string `json:"profile,omitempty"` // audio: "dolby digital plus + dolby atmos", "ma"
	Lang     string `json:"lang,omitempty"`    // BCP 47 tag: fr, en
	Title    string `json:"title,omitempty"`   // as named in the file
	Layout   string `json:"layout,omitempty"`  // audio: 5.1, 7.1
	Forced   bool   `json:"forced,omitempty"`
	HI       bool   `json:"hi,omitempty"` // subtitles for the deaf and hard of hearing
	Default  bool   `json:"default,omitempty"`
	External bool   `json:"external,omitempty"` // subtitle in a separate file
}

// Tracks returns the audio and subtitle tracks of each version of an item.
func (s *Server) Tracks(ctx context.Context, ratingKey string) ([]Version, error) {
	var v struct {
		MediaContainer struct {
			Metadata []struct {
				Media []struct {
					VideoResolution string `json:"videoResolution"` // 4k, 1080, sd
					Part            []struct {
						Size   int64 `json:"size"`
						Stream []struct {
							StreamType         int    `json:"streamType"`
							Codec              string `json:"codec"`
							Profile            string `json:"profile"`
							BitDepth           int    `json:"bitDepth"`
							DOVIPresent        bool   `json:"DOVIPresent"`
							ColorTrc           string `json:"colorTrc"`
							DisplayTitle       string `json:"displayTitle"`
							LanguageTag        string `json:"languageTag"`
							Title              string `json:"title"`
							AudioChannelLayout string `json:"audioChannelLayout"`
							Forced             bool   `json:"forced"`
							HearingImpaired    bool   `json:"hearingImpaired"`
							Default            bool   `json:"default"`
							Key                string `json:"key"`
						} `json:"Stream"`
					} `json:"Part"`
				} `json:"Media"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := s.get(ctx, "/library/metadata/"+url.PathEscape(ratingKey), nil, &v); err != nil {
		return nil, err
	}
	var out []Version
	for _, m := range v.MediaContainer.Metadata {
		for _, med := range m.Media {
			ver := Version{Resolution: resolution(med.VideoResolution)}
			for _, p := range med.Part {
				ver.Size += p.Size
				for _, st := range p.Stream {
					t := Track{Codec: st.Codec, Lang: st.LanguageTag, Title: st.Title, Forced: st.Forced,
						HI: st.HearingImpaired, Default: st.Default}
					switch st.StreamType {
					case 1:
						if ver.VideoCodec == "" {
							ver.VideoCodec, ver.BitDepth = st.Codec, st.BitDepth
							ver.HDR = hdrOf(st.DOVIPresent, st.ColorTrc, st.DisplayTitle)
						}
					case 2:
						t.Profile = st.Profile
						t.Layout = strings.SplitN(st.AudioChannelLayout, "(", 2)[0]
						ver.Audio = append(ver.Audio, t)
					case 3:
						t.External = st.Key != ""
						ver.Subs = append(ver.Subs, t)
					}
				}
			}
			out = append(out, ver)
		}
	}
	return out, nil
}

// hdrOf names the dynamic range Plex found in a video stream: Dolby Vision
// from its own flag, HDR10+ from Plex's display title (no flag for it), HDR10
// and HLG from the transfer function.
func hdrOf(dovi bool, trc, display string) []string {
	var out []string
	if dovi {
		out = append(out, "DV")
	}
	switch {
	case strings.Contains(display, "HDR10+"):
		out = append(out, "HDR10+")
	case trc == "smpte2084":
		out = append(out, "HDR10")
	case trc == "arib-std-b67":
		out = append(out, "HLG")
	}
	return out
}

// resolution names Plex's videoResolution ("4k", "1080", "sd").
func resolution(r string) string {
	switch {
	case r == "":
		return ""
	case strings.EqualFold(r, "4k"):
		return "4K"
	case strings.EqualFold(r, "sd"):
		return "SD"
	case strings.HasSuffix(r, "p"):
		return r
	default:
		return r + "p"
	}
}

// EpisodeKey finds the ratingKey of one episode of a show.
func (s *Server) EpisodeKey(ctx context.Context, showKey string, season, episode int) (string, error) {
	var v struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey   string `json:"ratingKey"`
				ParentIndex int    `json:"parentIndex"`
				Index       int    `json:"index"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := s.get(ctx, "/library/metadata/"+url.PathEscape(showKey)+"/allLeaves", nil, &v); err != nil {
		return "", err
	}
	for _, m := range v.MediaContainer.Metadata {
		if m.ParentIndex == season && m.Index == episode {
			return m.RatingKey, nil
		}
	}
	return "", errNotFound
}
