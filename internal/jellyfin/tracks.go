package jellyfin

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/neotoxicfr/agendarr/internal/calendar"
	"github.com/neotoxicfr/agendarr/internal/plex"
)

// mediaSource is one file (version) of an item, as Jellyfin probed it.
type mediaSource struct {
	Size    int64 `json:"Size"`
	Streams []struct {
		Type               string `json:"Type"` // Video, Audio, Subtitle
		Codec              string `json:"Codec"`
		Profile            string `json:"Profile"`
		Width              int    `json:"Width"`
		BitDepth           int    `json:"BitDepth"`
		VideoRangeType     string `json:"VideoRangeType"`
		ChannelLayout      string `json:"ChannelLayout"`
		AudioSpatialFormat string `json:"AudioSpatialFormat"`
		Language           string `json:"Language"`
		Title              string `json:"Title"`
		IsForced           bool   `json:"IsForced"`
		IsHearingImpaired  bool   `json:"IsHearingImpaired"`
		IsDefault          bool   `json:"IsDefault"`
		IsExternal         bool   `json:"IsExternal"`
	} `json:"MediaStreams"`
}

// Tracks returns the versions of an item in the plex package's shape, which
// the browser already renders; nil when the item is gone.
func (c *Client) Tracks(ctx context.Context, itemID string) ([]plex.Version, error) {
	var out []plex.Version
	err := c.items(ctx, url.Values{"ids": {itemID}, "fields": {"MediaSources"}}, 1, func(it item) bool {
		out = versions(it.MediaSources)
		return false
	})
	return out, err
}

// Episode is one episode of a season, with its versions.
type Episode struct {
	ID       string
	Season   int
	Episode  int
	Versions []plex.Version
}

// SeasonTracks returns the episodes of one season with their tracks, in one call.
func (c *Client) SeasonTracks(ctx context.Context, seriesID string, season int) ([]Episode, error) {
	var v struct {
		Items []item `json:"Items"`
	}
	q := url.Values{"season": {strconv.Itoa(season)}, "fields": {"MediaSources"}}
	if err := c.do(ctx, http.MethodGet, "/Shows/"+url.PathEscape(seriesID)+"/Episodes", q, nil, &v); err != nil {
		return nil, err
	}
	out := make([]Episode, 0, len(v.Items))
	for _, it := range v.Items {
		out = append(out, Episode{ID: it.ID, Season: it.Season, Episode: it.Episode, Versions: versions(it.MediaSources)})
	}
	return out, nil
}

func versions(sources []mediaSource) []plex.Version {
	var out []plex.Version
	for _, src := range sources {
		v := plex.Version{Size: src.Size}
		for _, st := range src.Streams {
			t := plex.Track{Codec: subCodec(st.Codec), Lang: lang(st.Language), Title: st.Title, Forced: st.IsForced,
				HI: st.IsHearingImpaired, Default: st.IsDefault}
			switch st.Type {
			case "Video":
				if v.VideoCodec == "" {
					v.VideoCodec, v.BitDepth = st.Codec, st.BitDepth
					v.Resolution, v.HDR = resolution(st.Width), hdr(st.VideoRangeType)
				}
			case "Audio":
				t.Codec, t.Profile = st.Codec, audioProfile(st.Profile, st.AudioSpatialFormat)
				t.Layout = strings.SplitN(st.ChannelLayout, "(", 2)[0]
				v.Audio = append(v.Audio, t)
			case "Subtitle":
				t.External = st.IsExternal
				v.Subs = append(v.Subs, t)
			}
		}
		out = append(out, v)
	}
	return out
}

// resolution names a frame by its width, which survives the cropping of
// wide films (3840x1600 is 4K), as calendar does for Radarr's files.
func resolution(width int) string {
	switch {
	case width <= 0:
		return ""
	case width >= 3800:
		return "4K"
	case width >= 2500:
		return "1440p"
	case width >= 1900:
		return "1080p"
	case width >= 1260:
		return "720p"
	default:
		return "SD"
	}
}

// hdr names a dynamic range as the plex package does: Dolby Vision first,
// then the HDR10+, HDR10 or HLG layer it carries.
func hdr(rangeType string) []string {
	var out []string
	r := strings.ToUpper(rangeType)
	if strings.HasPrefix(r, "DOVI") {
		out = append(out, "DV")
	}
	switch {
	case strings.HasSuffix(r, "HDR10PLUS"):
		out = append(out, "HDR10+")
	case strings.HasSuffix(r, "HDR10"):
		out = append(out, "HDR10")
	case strings.HasSuffix(r, "HLG"):
		out = append(out, "HLG")
	}
	return out
}

// audioProfile writes the profile the way Plex does, which formats.js reads:
// "ma", "hra", "x", "es" for DTS, "atmos" when Jellyfin found Atmos.
func audioProfile(profile, spatial string) string {
	p := strings.ToLower(profile)
	// Whole words: "Main" (an AAC profile) is not "MA".
	words := strings.FieldsFunc(p, func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') })
	out := ""
	switch {
	case strings.Contains(p, "dts:x") || strings.Contains(p, "dts-x"):
		out = "x"
	case slices.Contains(words, "ma"):
		out = "ma"
	case slices.Contains(words, "hra"):
		out = "hra"
	case slices.Contains(words, "es"):
		out = "es"
	}
	if strings.EqualFold(spatial, "DolbyAtmos") || strings.Contains(p, "atmos") {
		out = strings.TrimSpace(out + " atmos")
	}
	return out
}

// subCodec names subtitle formats as Plex does.
func subCodec(c string) string {
	switch strings.ToLower(c) {
	case "subrip":
		return "srt"
	case "pgssub", "hdmv_pgs_subtitle":
		return "pgs"
	case "dvd_subtitle", "dvdsub":
		return "vobsub"
	case "webvtt":
		return "vtt"
	}
	return strings.ToLower(c)
}

func lang(code string) string {
	if code == "" || code == "und" {
		return ""
	}
	return calendar.LangTag(code)
}
