package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/neotoxicfr/agendarr/internal/jellyfin"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// One media server per install: Plex (the default, so installs from before
// Jellyfin need nothing) or Jellyfin, chosen at first run. A Jellyfin server
// reuses keyServerID and keyServerName for its id and name.
const (
	keyMediaServer = "media_server" // "plex" | "jellyfin"
	keyJFURL       = "jellyfin_url"
	keyJFPublic    = "jellyfin_public_url" // optional, for "Open in Jellyfin" links
	keyJFKey       = "jellyfin_api_key"    // sealed

	sessJFSetup  = "jf_setup"  // first run: the server URL chosen, before sign-in
	sessJFPublic = "jf_public" // first run: its optional public URL
	sessJFSecret = "jf_secret" // pending Quick Connect secret
	sessJFDevice = "jf_device"
)

// isJellyfin reports whether this install uses a Jellyfin server.
func (s *Server) isJellyfin(ctx context.Context) bool {
	if v, ok := s.mediaKind.Load().(string); ok {
		return v == "jellyfin"
	}
	// Read setupDone first: a value read before the claim commits must not be
	// cached after it (EDGE-a2-07).
	done := s.setupDone(ctx)
	v, err := s.store.Setting(ctx, keyMediaServer)
	if err != nil {
		return false
	}
	if done {
		s.mediaKind.Store(v) // fixed once the install is set up
	}
	return v == "jellyfin"
}

// onlyServer serves h only for the install's media server, once set up;
// before setup both sign-ins are open (Plex directly, Jellyfin after its
// server was chosen).
func (s *Server) onlyServer(kind string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.setupDone(r.Context()) && s.isJellyfin(r.Context()) != (kind == "jellyfin") {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}
}

// jellyfinAt returns a client for the server at base; token may be empty.
func (s *Server) jellyfinAt(base, token, device string) *jellyfin.Client {
	return &jellyfin.Client{BaseURL: base, Token: token, DeviceID: device, Version: Version}
}

// jellyfinClient reads the bound Jellyfin server with its API key; nil on a
// Plex install.
func (s *Server) jellyfinClient(ctx context.Context) (*jellyfin.Client, error) {
	if !s.setupDone(ctx) || !s.isJellyfin(ctx) {
		return nil, nil
	}
	base, err := s.store.Setting(ctx, keyJFURL)
	if err != nil {
		return nil, err
	}
	sealed, err := s.store.Setting(ctx, keyJFKey)
	if err != nil {
		return nil, err
	}
	key, err := s.box.Open(sealed)
	if err != nil || key == "" || base == "" {
		return nil, errors.New("jellyfin: no server or API key")
	}
	return s.jellyfinAt(base, key, s.plex.ClientID), nil
}

// newDevice names one sign-in attempt: Jellyfin keeps a session per device.
func (s *Server) newDevice() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return s.plex.ClientID + "-" + hex.EncodeToString(b)
}

// jellyfinBase is the server to sign in to: the bound one, or on first run
// the one chosen on the setup page.
func (s *Server) jellyfinBase(r *http.Request) string {
	if !s.setupDone(r.Context()) {
		return s.sessions.GetString(r.Context(), sessJFSetup)
	}
	base, _ := s.store.Setting(r.Context(), keyJFURL)
	return base
}

// handleSetupJellyfin records the Jellyfin server chosen on first run, once
// it answers as one. Only before setup: this is the one request a visitor
// without an account can make the server send.
func (s *Server) handleSetupJellyfin(w http.ResponseWriter, r *http.Request) {
	if s.setupDone(r.Context()) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.PostFormValue("reset") != "" {
		s.sessions.Remove(r.Context(), sessJFSetup)
		s.sessions.Remove(r.Context(), sessJFPublic)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	base, okBase := httpURL(r.PostFormValue("url"))
	public, okPublic := httpURL(r.PostFormValue("public"))
	if !okBase || (r.PostFormValue("public") != "" && !okPublic) {
		http.Redirect(w, r, "/login?e=jf_url", http.StatusSeeOther)
		return
	}
	if !s.pinLimit.allow(time.Now()) {
		http.Redirect(w, r, "/login?e=jf_unreachable", http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := s.jellyfinAt(base, "", s.newDevice()).PublicInfo(ctx); err != nil {
		s.log.Warn("jellyfin setup", "err", err)
		http.Redirect(w, r, "/login?e=jf_unreachable", http.StatusSeeOther)
		return
	}
	s.sessions.Put(r.Context(), sessJFSetup, base)
	s.sessions.Put(r.Context(), sessJFPublic, public)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleAdminJellyfin changes the Jellyfin server's addresses. Only the
// owner may, as only the owner bound the server: the API key and everyone's
// sign-in go to that address. The change is kept only if the server there
// has the same public id and then accepts the API key with that same id.
func (s *Server) handleAdminJellyfin(w http.ResponseWriter, r *http.Request) {
	if u := currentUser(r); u == nil || !u.IsOwner {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	base, okBase := httpURL(r.PostFormValue("url"))
	public, okPublic := httpURL(r.PostFormValue("public"))
	switch {
	case !okBase || (r.PostFormValue("public") != "" && !okPublic):
		s.flash(r, "err.jf_url")
	default:
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		id, _ := s.store.Setting(ctx, keyServerID)
		info, err := s.jellyfinAt(base, "", s.plex.ClientID).PublicInfo(ctx)
		if err == nil && info.ID == id {
			// Same public id: now check it holds our key, before saving.
			var jf *jellyfin.Client
			if jf, err = s.jellyfinClient(ctx); err == nil {
				jf.BaseURL = base
				var auth jellyfin.Info
				if auth, err = jf.SystemInfo(ctx); err == nil && auth.ID != id {
					info.ID = auth.ID
				}
			}
		}
		switch {
		case errors.Is(err, jellyfin.ErrUnauthorized) || (err == nil && info.ID != id):
			s.flash(r, "admin.jf_other_server")
		case err != nil:
			s.flash(r, "err.jf_unreachable")
		default:
			for k, v := range map[string]string{keyJFURL: base, keyJFPublic: public, keyServerName: info.Name} {
				if err := s.store.SetSetting(ctx, k, v); err != nil {
					s.fail(w, r, err)
					return
				}
			}
			s.resetLibrary()
			s.flash(r, "admin.jf_saved")
		}
	}
	http.Redirect(w, r, "/admin/instances", http.StatusSeeOther)
}

// httpURL keeps an absolute http(s) URL without a trailing slash.
func httpURL(raw string) (string, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", false
	}
	return raw, true
}

// handleQCStart asks the server for a Quick Connect code to show.
func (s *Server) handleQCStart(w http.ResponseWriter, r *http.Request) {
	base := s.jellyfinBase(r)
	if base == "" {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": "/login"})
		return
	}
	if !s.pinLimit.allow(time.Now()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"redirect": "/login?e=jf_unreachable"})
		return
	}
	// Bounded so a dead server answers at once, not after two 30 s calls (UX-a2-04).
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	device := s.newDevice()
	c := s.jellyfinAt(base, "", device)
	if on, err := c.QuickConnectEnabled(ctx); err == nil && !on {
		writeJSON(w, http.StatusOK, map[string]any{"disabled": true})
		return
	}
	qc, err := c.Initiate(ctx)
	switch {
	case err == nil:
	case errors.Is(err, jellyfin.ErrNotFound) || errors.Is(err, jellyfin.ErrUnauthorized):
		writeJSON(w, http.StatusOK, map[string]any{"disabled": true})
		return
	default:
		s.log.Warn("quick connect", "err", err)
		writeJSON(w, http.StatusOK, map[string]string{"redirect": "/login?e=jf_unreachable"})
		return
	}
	s.sessions.Put(r.Context(), sessJFSecret, qc.Secret)
	s.sessions.Put(r.Context(), sessJFDevice, device)
	writeJSON(w, http.StatusOK, map[string]string{"code": qc.Code})
}

// handleQCCheck is polled until the code is approved in a Jellyfin app.
func (s *Server) handleQCCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	secret, device, base := s.sessions.GetString(ctx, sessJFSecret), s.sessions.GetString(ctx, sessJFDevice), s.jellyfinBase(r)
	if secret == "" || base == "" {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": "/login?e=expired"})
		return
	}
	// Polls hit the household server: an empty bucket answers "pending" without
	// an upstream call (SEC-a2-01). Each code has its own, so a client looping
	// on its check does not stall the other sign-ins.
	if now := time.Now(); !s.checkOne.allow(secret, now) || !s.checkLimit.allow(now) {
		writeJSON(w, http.StatusOK, map[string]any{"pending": true})
		return
	}
	c := s.jellyfinAt(base, "", device)
	ok, err := c.Approved(ctx, secret)
	switch {
	case errors.Is(err, jellyfin.ErrNotFound) || errors.Is(err, jellyfin.ErrUnauthorized):
		s.sessions.Remove(ctx, sessJFSecret) // a dead code must not keep relaying polls (SEC-a2-01)
		writeJSON(w, http.StatusOK, map[string]string{"redirect": "/login?e=expired"})
		return
	case err != nil:
		s.log.Warn("quick connect check", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"pending": true}) // keep polling through a blip
		return
	case !ok:
		writeJSON(w, http.StatusOK, map[string]any{"pending": true})
		return
	}
	s.sessions.Remove(ctx, sessJFSecret)
	a, err := c.AuthQuickConnect(ctx, secret)
	if err != nil {
		s.log.Warn("quick connect auth", "err", err)
		writeJSON(w, http.StatusOK, map[string]string{"redirect": "/login?e=expired"})
		return
	}
	next, code := s.completeJellyfinSignIn(r, base, device, a)
	if code != "" {
		next = "/login?e=" + code
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": next})
}

// handleJFPassword signs in with a name and password, passed on to the
// server and never kept.
func (s *Server) handleJFPassword(w http.ResponseWriter, r *http.Request) {
	base := s.jellyfinBase(r)
	if base == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	// An over-long body fails to parse: refuse it rather than send blanks.
	err := r.ParseForm()
	user, pass := r.PostFormValue("username"), r.PostFormValue("password")
	if err != nil || len(user) > 128 || len(pass) > 256 {
		http.Redirect(w, r, "/login?e=jf_bad_login", http.StatusSeeOther)
		return
	}
	// Passwords get slow buckets of their own, so guessing is not worth an
	// Internet-side brute force and cannot drain Quick Connect's. One per
	// username: guesses on an account do not lock the others out (SEC-a2-02).
	if now := time.Now(); !s.pwLimit.allow(strings.ToLower(user), now) || !s.pwAll.allow(now) {
		http.Redirect(w, r, "/login?e=jf_wait", http.StatusSeeOther)
		return
	}
	device := s.newDevice()
	a, err := s.jellyfinAt(base, "", device).AuthByName(r.Context(), user, pass)
	var next, code string
	switch {
	case errors.Is(err, jellyfin.ErrUnauthorized):
		s.log.Info("sign-in refused: bad Jellyfin password", "user", user[:min(len(user), 64)])
		code = "jf_bad_login"
	case err != nil:
		s.log.Warn("jellyfin password sign-in", "err", err)
		code = "jf_unreachable"
	default:
		next, code = s.completeJellyfinSignIn(r, base, device, a)
	}
	if code != "" {
		next = "/login?e=" + code
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// completeJellyfinSignIn turns a Jellyfin sign-in into an agendarr session.
// On first run the account must be a Jellyfin administrator: its token mints
// the API key agendarr reads everyone's lists with, and it becomes the owner.
// The user's own token is then signed out: agendarr keeps none.
func (s *Server) completeJellyfinSignIn(r *http.Request, base, device string, a jellyfin.Auth) (next, code string) {
	ctx := r.Context()
	c := s.jellyfinAt(base, a.Token, device)
	defer func() {
		if err := c.Logout(context.WithoutCancel(ctx)); err != nil {
			s.log.Warn("jellyfin logout", "err", err)
		}
	}()
	if a.User.Policy.IsDisabled {
		return "", "no_access"
	}
	profile := store.Profile{AccountID: jellyfin.FormatID(a.User.ID), Username: a.User.Name}

	if !s.setupDone(ctx) {
		if !a.User.Policy.IsAdministrator {
			return "", "jf_not_admin"
		}
		// Every check runs before the key exists, so a failure leaves none on the
		// server (SEC-a2-05).
		info, err := c.PublicInfo(ctx)
		if err != nil || info.ID != a.ServerID {
			s.log.Error("jellyfin server", "err", err)
			return "", "jf_unreachable"
		}
		u, err := s.store.SignIn(ctx, profile, time.Now())
		if err != nil {
			s.log.Error("sign in", "err", err)
			return "", "jf_unreachable"
		}
		key, err := c.CreateKey(ctx, "Agendarr")
		if err != nil {
			s.log.Error("jellyfin api key", "err", err)
			_ = s.store.DeleteUser(ctx, u.ID) // (EDGE-a2-06)
			return "", "jf_unreachable"
		}
		settings := map[string]string{keyMediaServer: "jellyfin", keyServerID: info.ID, keyServerName: info.Name,
			keyJFURL: base, keyJFPublic: s.sessions.GetString(ctx, sessJFPublic), keyJFKey: s.box.Seal(key)}
		if err := s.store.ClaimOwner(ctx, u.ID, settings); err != nil {
			s.log.Error("claim owner", "err", err)
			_ = s.store.DeleteUser(ctx, u.ID) // lost the race: leave no stray account (EDGE-a2-06)
			return "", "setup_taken"
		}
		s.ownerKnown.Store(true)
		s.mediaKind.Store("jellyfin")
		s.resetLibrary()
		s.sessions.Remove(ctx, sessJFSetup)
		s.sessions.Remove(ctx, sessJFPublic)
		s.signIn(r, u.ID)
		return "/admin", ""
	}

	serverID, _ := s.store.Setting(ctx, keyServerID)
	if a.ServerID != serverID {
		s.log.Info("sign-in refused: another Jellyfin server", "user", a.User.Name)
		return "", "no_access"
	}
	u, err := s.store.SignIn(ctx, profile, time.Now())
	if err != nil {
		s.log.Error("sign in", "err", err)
		return "", "jf_unreachable"
	}
	s.signIn(r, u.ID)
	return "/", ""
}
