package calendar

import (
	"strconv"
	"strings"

	"github.com/neotoxicfr/agendarr/internal/arr"
)

// mediaOf sums up the file Radarr or Sonarr has; nil without one.
func mediaOf(instName string, f *arr.File) []Media {
	if f == nil {
		return nil
	}
	m := Media{Instance: instName, Quality: f.Quality.Quality.Name, Size: f.Size}
	if mi := f.MediaInfo; mi != nil {
		m.Resolution = resolutionLabel(mi.Resolution)
		m.Video = mi.VideoCodec
		if mi.VideoBitDepth > 8 && m.Video != "" {
			m.Video += " " + strconv.Itoa(mi.VideoBitDepth) + " bit"
		}
		m.HDR = mi.VideoDynamicRangeType
		m.Audio = mi.AudioCodec
		if mi.AudioChannels > 0 && m.Audio != "" {
			m.Audio += " " + strconv.FormatFloat(mi.AudioChannels, 'f', 1, 64)
		}
		m.AudioLangs = languages(mi.AudioLanguages)
		m.Subs = languages(mi.Subtitles)
	}
	return []Media{m}
}

// resolutionLabel names a "3840x1606" frame by its width, which survives
// the cropping of wide films.
func resolutionLabel(res string) string {
	w, _, ok := strings.Cut(res, "x")
	width, err := strconv.Atoi(w)
	if !ok || err != nil || width <= 0 {
		return ""
	}
	switch {
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

// languages turns "fre/fre/eng" into ["fr", "en"]: deduplicated, in order,
// as two-letter codes where one exists, for Intl.DisplayNames.
func languages(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range strings.Split(s, "/") {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || c == "und" || c == "zxx" || len(out) == 12 {
			continue
		}
		if two, ok := iso6392[c]; ok {
			c = two
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// iso6392 maps the ISO 639-2 codes (bibliographic and terminology forms)
// of common languages to ISO 639-1.
var iso6392 = map[string]string{
	"ara": "ar", "chi": "zh", "zho": "zh", "cze": "cs", "ces": "cs", "dan": "da", "dut": "nl", "nld": "nl",
	"eng": "en", "fin": "fi", "fre": "fr", "fra": "fr", "ger": "de", "deu": "de", "gre": "el", "ell": "el",
	"heb": "he", "hin": "hi", "hun": "hu", "ice": "is", "isl": "is", "ind": "id", "ita": "it", "jpn": "ja",
	"kor": "ko", "may": "ms", "msa": "ms", "nor": "no", "nob": "nb", "nno": "nn", "per": "fa", "fas": "fa",
	"pol": "pl", "por": "pt", "rum": "ro", "ron": "ro", "rus": "ru", "slo": "sk", "slk": "sk", "spa": "es",
	"swe": "sv", "tha": "th", "tur": "tr", "ukr": "uk", "vie": "vi", "bul": "bg", "hrv": "hr", "srp": "sr",
	"slv": "sl", "est": "et", "lav": "lv", "lit": "lt", "cat": "ca", "baq": "eu", "eus": "eu", "glg": "gl",
	"wel": "cy", "cym": "cy", "gle": "ga", "tam": "ta", "tel": "te", "fil": "fil", "tgl": "tl",
}
