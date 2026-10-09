package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/neotoxicfr/agendarr/internal/calendar"
	"github.com/neotoxicfr/agendarr/internal/personal"
	"github.com/neotoxicfr/agendarr/internal/store"
	"github.com/neotoxicfr/agendarr/internal/webpush"
)

// The release watcher looks at the calendar every few minutes and notifies
// people when a release they care about becomes available, or when its file
// is replaced by a different one (an upgrade). People care about what they
// requested in Seerr, the shows they are watching (their choice, in the
// notification panel) and what they follow by hand from a detail sheet.
const (
	watchEvery = 5 * time.Minute
	// The window around today that is watched: a release usually lands
	// around its date; an upgrade can come a few weeks later.
	watchBack  = 30 * 24 * time.Hour
	watchAhead = 14 * 24 * time.Hour
)

// notifySources are the automatic sources a user can pick; both by default.
var notifySources = []string{personal.SourceSeerr, personal.SourceWatching, personal.SourceWatchlist}

var defaultNotify = []string{personal.SourceSeerr, personal.SourceWatching}

func cleanNotify(in []string) []string {
	if in == nil {
		return slices.Clone(defaultNotify)
	}
	out := []string{}
	for _, v := range in {
		if slices.Contains(notifySources, v) && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// RunWatcher checks releases until ctx is done.
func (s *Server) RunWatcher(ctx context.Context) {
	t := time.NewTimer(time.Minute) // let the caches warm up first
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.checkReleases(ctx, time.Now()); err != nil {
			s.log.Warn("release watcher", "err", err)
		}
		t.Reset(watchEvery)
	}
}

// stateKey identifies a file: the movie (all its release dates share one),
// or the episode.
func stateKey(e calendar.Event) string {
	if e.Kind == "movie" {
		return e.Group
	}
	return e.ID
}

// mediaSig sums up the files of a release; "" when nothing is known.
func mediaSig(e calendar.Event) string {
	var parts []string
	for _, m := range e.Media {
		parts = append(parts, strings.Join([]string{m.Instance, m.Resolution, m.HDR, m.VideoCodec, m.AudioCodec, m.Channels,
			strings.Join(m.AudioLangs, "+"), strings.Join(m.Subs, "+")}, "|"))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

// change is what happened to one movie or series in one check.
type change struct {
	event    calendar.Event   // a representative event (title, poster, keys)
	kind     string           // available | upgraded
	episodes []calendar.Event // episodes concerned (series)
	diffs    []diff           // upgrades: what changed
}

// diff is one difference between two files, rendered by the browser.
type diff struct {
	What string   `json:"what"` // resolution | hdr | video | audio | audioLangs | subs
	From string   `json:"from,omitempty"`
	To   string   `json:"to,omitempty"`
	Add  []string `json:"add,omitempty"`
}

// diffMedia compares the first file of two states.
func diffMedia(prev, cur calendar.Media) []diff {
	var out []diff
	for _, f := range []struct{ what, from, to string }{
		{"resolution", prev.Resolution, cur.Resolution},
		{"hdr", prev.HDR, cur.HDR},
		{"video", prev.VideoCodec, cur.VideoCodec},
		{"audio", strings.TrimSpace(prev.AudioCodec + " " + prev.Channels), strings.TrimSpace(cur.AudioCodec + " " + cur.Channels)},
	} {
		if f.from != f.to && f.to != "" {
			out = append(out, diff{What: f.what, From: f.from, To: f.to})
		}
	}
	added := func(a, b []string) (out []string) {
		for _, x := range b {
			if !slices.Contains(a, x) {
				out = append(out, x)
			}
		}
		return out
	}
	if add := added(prev.AudioLangs, cur.AudioLangs); len(add) > 0 {
		out = append(out, diff{What: "audioLangs", Add: add})
	}
	if add := added(prev.Subs, cur.Subs); len(add) > 0 {
		out = append(out, diff{What: "subs", Add: add})
	}
	return out
}

// sigMedia reads back the first file of a signature written by mediaSig.
func sigMedia(sig string) calendar.Media {
	line, _, _ := strings.Cut(sig, "\n")
	f := strings.Split(line, "|")
	if len(f) != 8 {
		return calendar.Media{}
	}
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(s, "+")
	}
	return calendar.Media{Instance: f[0], Resolution: f[1], HDR: f[2], VideoCodec: f[3], AudioCodec: f[4], Channels: f[5],
		AudioLangs: split(f[6]), Subs: split(f[7])}
}

func (s *Server) checkReleases(ctx context.Context, now time.Time) error {
	res, err := s.cal.Range(ctx, now.Add(-watchBack), now.Add(watchAhead))
	if err != nil || !res.Configured {
		return err
	}
	prev, err := s.store.MediaStates(ctx)
	if err != nil {
		return err
	}
	cur := map[string]store.MediaState{}
	byGroup := map[string]*change{}
	for _, e := range res.Events {
		k := stateKey(e)
		if _, done := cur[k]; done {
			continue
		}
		st := store.MediaState{HasFile: e.HasFile, Sig: mediaSig(e)}
		cur[k] = st
		p, known := prev[k]
		var kind string
		switch {
		case !known:
			// First sight: remember it, nothing to say.
		case !p.HasFile && st.HasFile:
			kind = "available"
		case p.HasFile && st.HasFile && p.Sig != "" && st.Sig != "" && p.Sig != st.Sig:
			kind = "upgraded"
		}
		if kind == "" {
			continue
		}
		c := byGroup[e.Group+"/"+kind]
		if c == nil {
			c = &change{event: e, kind: kind}
			byGroup[e.Group+"/"+kind] = c
		}
		if e.Kind == "episode" {
			c.episodes = append(c.episodes, e)
		}
		if kind == "upgraded" && len(c.diffs) == 0 {
			c.diffs = diffMedia(sigMedia(p.Sig), sigMedia(st.Sig))
		}
	}
	if err := s.store.SaveMediaStates(ctx, cur, now); err != nil {
		return err
	}
	if len(byGroup) == 0 {
		return nil
	}
	return s.deliver(ctx, byGroup, now)
}

// deliver records and pushes each change to the people it concerns.
func (s *Server) deliver(ctx context.Context, changes map[string]*change, now time.Time) error {
	users, err := s.store.Users(ctx)
	if err != nil {
		return err
	}
	followers, err := s.store.Followers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.Disabled {
			continue
		}
		sources := cleanNotify(s.loadPrefs(ctx, u.ID).Notify)
		var keys map[string]bool
		if len(sources) > 0 {
			keys = s.personal.For(ctx, u, sources).Keys
		}
		for _, c := range changes {
			if !slices.Contains(followers[c.event.Group], u.ID) && !anyKey(c.event.Keys, keys) {
				continue
			}
			if err := s.notifyUser(ctx, u, c, now); err != nil {
				s.log.Warn("notify", "user", u.Username, "err", err)
			}
		}
	}
	return nil
}

func anyKey(keys []string, set map[string]bool) bool {
	for _, k := range keys {
		if set[k] {
			return true
		}
	}
	return false
}

// notifyDetail is what the browser needs to render a notification.
type notifyDetail struct {
	Kind     string    `json:"kind"` // movie | episode
	Poster   string    `json:"poster,omitempty"`
	Release  string    `json:"release,omitempty"`
	Episodes []epRef   `json:"episodes,omitempty"`
	Diffs    []diff    `json:"diffs,omitempty"`
	Date     string    `json:"date,omitempty"`
	Air      time.Time `json:"air,omitzero"`
}

type epRef struct {
	Season  int    `json:"s"`
	Episode int    `json:"e"`
	Title   string `json:"t,omitempty"`
}

func (s *Server) notifyUser(ctx context.Context, u store.User, c *change, now time.Time) error {
	d := notifyDetail{Kind: c.event.Kind, Poster: c.event.Poster, Release: c.event.Release, Diffs: c.diffs, Date: c.event.Date}
	sort.Slice(c.episodes, func(i, j int) bool {
		a, b := c.episodes[i], c.episodes[j]
		return a.Season < b.Season || (a.Season == b.Season && a.Episode < b.Episode)
	})
	for _, e := range c.episodes {
		d.Episodes = append(d.Episodes, epRef{e.Season, e.Episode, e.EpTitle})
	}
	raw, _ := json.Marshal(d)
	if _, err := s.store.AddNotification(ctx, u.ID, store.Notification{At: now, Kind: c.kind, Group: c.event.Group,
		Title: c.event.Title, Detail: string(raw)}); err != nil {
		return err
	}
	return s.pushTo(ctx, u.ID, c, d)
}

// pushTo sends a Web Push message to each browser of the user, in its language.
func (s *Server) pushTo(ctx context.Context, userID int64, c *change, d notifyDetail) error {
	sender, err := s.pushSender(ctx)
	if err != nil || sender == nil {
		return err
	}
	subs, err := s.store.PushSubs(ctx, userID)
	if err != nil {
		return err
	}
	for _, p := range subs {
		payload, _ := json.Marshal(map[string]string{
			"title": c.event.Title, "body": s.pushBody(p.Lang, c, d), "tag": c.kind + ":" + c.event.Group, "url": "/",
			"icon": "/static/icons/icon-192.png",
		})
		var sub webpush.Subscription
		sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth = p.Endpoint, p.P256dh, p.Auth
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := sender.Send(pctx, sub, payload)
		cancel()
		if errors.Is(err, webpush.ErrGone) {
			_ = s.store.DeletePushSub(ctx, 0, p.Endpoint)
		} else if err != nil {
			s.log.Info("web push", "err", err)
		}
	}
	return nil
}

// pushBody is the one-line text of a push message.
func (s *Server) pushBody(lang string, c *change, d notifyDetail) string {
	T := func(k string) string { return s.i18n.T(lang, k) }
	code := func(e epRef) string { return fmt.Sprintf("S%02dE%02d", e.Season, e.Episode) }
	var what string
	switch n := len(d.Episodes); {
	case n == 1:
		what = code(d.Episodes[0])
		if d.Episodes[0].Title != "" {
			what += " · " + d.Episodes[0].Title
		}
	case n > 1:
		what = code(d.Episodes[0]) + "–" + code(d.Episodes[n-1]) + " (" + strings.ReplaceAll(T("notif.n_episodes"), "{n}", fmt.Sprint(n)) + ")"
	}
	head := T("notif." + c.kind)
	if c.kind == "upgraded" {
		for _, df := range d.Diffs {
			if df.What == "resolution" || df.What == "hdr" || df.What == "video" || df.What == "audio" {
				sep := ": "
				if strings.HasPrefix(lang, "fr") {
					sep = " : "
				}
				head += sep + strings.TrimSpace(df.From+" → "+df.To)
				break
			}
		}
	}
	if what == "" {
		return head
	}
	return what + " — " + head
}

// pushSender returns the Web Push sender, creating the VAPID keys on first
// use; nil when push cannot work (no public URL to name ourselves).
func (s *Server) pushSender(ctx context.Context) (*webpush.Sender, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if s.push != nil {
		return s.push, nil
	}
	keys, err := s.vapidKeys(ctx)
	if err != nil {
		return nil, err
	}
	subject := s.cfg.BaseURL
	if subject == "" {
		subject = "https://github.com/neotoxicfr/agendarr"
	}
	s.push = &webpush.Sender{Keys: keys, Subject: subject, HTTP: &http.Client{Timeout: 20 * time.Second}}
	return s.push, nil
}

const keyVAPID = "vapid_private" // sealed

func (s *Server) vapidKeys(ctx context.Context) (webpush.Keys, error) {
	sealed, err := s.store.Setting(ctx, keyVAPID)
	if err != nil {
		return webpush.Keys{}, err
	}
	if sealed != "" {
		if raw, err := s.box.Open(sealed); err == nil {
			return webpush.ParseKeys(raw)
		}
	}
	k, err := webpush.NewKeys()
	if err != nil {
		return webpush.Keys{}, err
	}
	return k, s.store.SetSetting(ctx, keyVAPID, s.box.Seal(k.Marshal()))
}

// handleNotifications serves the bell: the history, the unread count, the
// key browsers subscribe with, and the automatic sources picked.
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	items, unread, err := s.store.Notifications(r.Context(), u.ID, 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := map[string]any{"items": items, "unread": unread, "notify": cleanNotify(s.loadPrefs(r.Context(), u.ID).Notify)}
	if sender, err := s.pushSender(r.Context()); err == nil && sender != nil {
		out["vapidKey"] = sender.Keys.PublicKey()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleNotificationsRead acknowledges {"ids":[1,2]}, or everything.
func (s *Server) handleNotificationsRead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || len(body.IDs) > 500 {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if err := s.store.MarkRead(r.Context(), currentUser(r).ID, body.IDs); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validGroup matches a movie or series group: "movie:tmdb:1", "series:tvdb:2".
func validGroup(g string) bool {
	kind, rest, ok := strings.Cut(g, ":")
	if !ok || (kind != "movie" && kind != "series") || len(g) > 80 {
		return false
	}
	src, id, ok := strings.Cut(rest, ":")
	if !ok || src == "" || id == "" {
		return false
	}
	return !strings.ContainsFunc(src+id, func(c rune) bool {
		return (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-'
	})
}

// handleFollow follows or unfollows {"group","title","on"}.
func (s *Server) handleFollow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Group string `json:"group"`
		Title string `json:"title"`
		On    bool   `json:"on"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || !validGroup(body.Group) {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(body.Title)
	if len(title) > 200 {
		title = title[:200]
	}
	if err := s.store.SetFollow(r.Context(), currentUser(r).ID, body.Group, title, body.On, time.Now()); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePush subscribes this browser ({"subscription":{…},"lang":"fr"}), or
// unsubscribes it ({"endpoint":"…","off":true}).
func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Subscription webpush.Subscription `json:"subscription"`
		Endpoint     string               `json:"endpoint"`
		Lang         string               `json:"lang"`
		Off          bool                 `json:"off"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	u := currentUser(r)
	if body.Off {
		if err := s.store.DeletePushSub(r.Context(), u.ID, body.Endpoint); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sub := body.Subscription
	if !webpush.AllowedEndpoint(sub.Endpoint) || len(sub.Endpoint) > 1000 || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		http.Error(w, "unsupported push service", http.StatusBadRequest)
		return
	}
	if _, err := webpush.Encrypt(sub, []byte("check")); err != nil {
		http.Error(w, "bad keys", http.StatusBadRequest)
		return
	}
	lang := body.Lang
	if !s.i18n.Has(lang) {
		lang = s.i18n.Pick(r)
	}
	if err := s.store.SavePushSub(r.Context(), store.PushSub{UserID: u.ID, Endpoint: sub.Endpoint, P256dh: sub.Keys.P256dh,
		Auth: sub.Keys.Auth, Lang: lang}, time.Now()); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleNotifySources saves the automatic sources picked in the bell.
func (s *Server) handleNotifySources(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Notify []string `json:"notify"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Notify == nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	u := currentUser(r)
	p := s.loadPrefs(r.Context(), u.ID)
	p.Notify = cleanNotify(body.Notify)
	raw, _ := json.Marshal(p)
	if err := s.store.SetPrefs(r.Context(), u.ID, string(raw)); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notify": p.Notify})
}
