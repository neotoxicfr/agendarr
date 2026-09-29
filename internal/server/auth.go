package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/neotoxicfr/agendarr/internal/i18n"
	"github.com/neotoxicfr/agendarr/internal/plex"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// Settings keys describing the Plex server agendarr is bound to.
const (
	keyPlexClientID    = "plex_client_id"
	keyServerID        = "plex_server_id"
	keyServerName      = "plex_server_name"
	keyServerToken     = "plex_server_token" // sealed
	keyServerConnsJSON = "plex_server_connections"
)

// Session keys.
const (
	sessUserID      = "uid"
	sessPinID       = "pin_id"
	sessSetupUserID = "setup_uid"
)

// signInErrors are the error codes carried in /login?e=; each has an
// "err.<code>" label.
var signInErrors = map[string]bool{
	"no_owned_server": true, "setup_denied": true, "no_access": true,
	"expired": true, "pending": true, "plex": true,
}

type ctxKey int

const userKey ctxKey = 0

// currentUser returns the signed-in user, or nil.
func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

// loadUser attaches the signed-in user to the request context. A user who
// lost access to the Plex server is signed out.
func (s *Server) loadUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := s.sessions.GetInt64(r.Context(), sessUserID); id != 0 {
			u, err := s.store.UserByID(r.Context(), id)
			switch {
			case err == nil && !u.Disabled:
				r = r.WithContext(context.WithValue(r.Context(), userKey, &u))
			case err == nil || errors.Is(err, store.ErrNotFound):
				_ = s.sessions.Destroy(r.Context())
			default:
				s.log.Error("load user", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireUser sends anonymous visitors to the sign-in page.
func (s *Server) requireUser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r) == nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// requireAdmin is requireUser plus admin rights.
func (s *Server) requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

func (s *Server) setupDone(ctx context.Context) bool {
	if s.ownerKnown.Load() {
		return true
	}
	ok, err := s.store.HasOwner(ctx)
	if err != nil {
		s.log.Error("has owner", "err", err)
		return false
	}
	if ok {
		s.ownerKnown.Store(true)
	}
	return ok
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	var d struct {
		Setup  bool
		Error  string
		Server string
	}
	d.Setup = !s.setupDone(r.Context())
	if code := r.URL.Query().Get("e"); signInErrors[code] {
		d.Error = "err." + code
	}
	d.Server, _ = s.store.Setting(r.Context(), keyServerName)
	s.render(w, r, http.StatusOK, "login", pageData{Title: "title.login", Data: d})
}

// baseURL is the public origin of the app.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// handlePinStart creates a Plex PIN and returns the two ways to approve it:
// a popup (desktop, and mobile tabs) that lands on a "you can close this"
// page, and a full redirect used when the popup is blocked.
func (s *Server) handlePinStart(w http.ResponseWriter, r *http.Request) {
	pin, err := s.plex.CreatePin(r.Context())
	if err != nil {
		s.log.Error("create pin", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"redirect": "/login?e=plex"})
		return
	}
	s.sessions.Put(r.Context(), sessPinID, pin.ID)
	base := s.baseURL(r)
	writeJSON(w, http.StatusOK, map[string]string{
		"popupUrl":    s.plex.AuthURL(pin.Code, base+"/auth/plex/done"),
		"redirectUrl": s.plex.AuthURL(pin.Code, base+"/auth/plex/return"),
	})
}

// handlePinCheck is polled by the sign-in page until Plex confirms.
func (s *Server) handlePinCheck(w http.ResponseWriter, r *http.Request) {
	next, code, pending := s.checkPin(r)
	switch {
	case pending:
		writeJSON(w, http.StatusOK, map[string]any{"pending": true})
	case code != "":
		writeJSON(w, http.StatusOK, map[string]any{"redirect": "/login?e=" + code})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"redirect": next})
	}
}

// handlePinReturn is where Plex sends the browser in the redirect flow.
// plex.tv may take a moment to attach the token to the PIN, hence the retries.
func (s *Server) handlePinReturn(w http.ResponseWriter, r *http.Request) {
	for i := 0; i < 5; i++ {
		next, code, pending := s.checkPin(r)
		if !pending {
			if code != "" {
				next = "/login?e=" + code
			}
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
	http.Redirect(w, r, "/login?e=pending", http.StatusSeeOther)
}

func (s *Server) handlePinDone(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "auth_done", pageData{Title: "title.login"})
}

// checkPin returns where to go next, or an error code, or pending.
func (s *Server) checkPin(r *http.Request) (next, code string, pending bool) {
	ctx := r.Context()
	id := s.sessions.GetInt64(ctx, sessPinID)
	if id == 0 {
		return "", "expired", false
	}
	pin, err := s.plex.CheckPin(ctx, id)
	if err != nil {
		s.log.Warn("check pin", "err", err)
		return "", "expired", false
	}
	if pin.AuthToken == "" {
		return "", "", true
	}
	s.sessions.Remove(ctx, sessPinID)
	next, code = s.completeSignIn(r, pin.AuthToken)
	return next, code, false
}

// completeSignIn turns a Plex token into an agendarr session. On a fresh
// install, the owner of a Plex server becomes the administrator and that
// server is bound to agendarr.
func (s *Server) completeSignIn(r *http.Request, token string) (next, code string) {
	ctx := r.Context()
	acc, err := s.plex.Account(ctx, token)
	if err != nil {
		s.log.Warn("plex account", "err", err)
		return "", "plex"
	}
	servers, err := s.plex.Servers(ctx, token)
	if err != nil {
		s.log.Warn("plex servers", "err", err)
		return "", "plex"
	}
	profile := store.Profile{PlexID: acc.ID, Username: acc.DisplayName(), Email: acc.Email, Thumb: acc.Thumb, PlexToken: s.box.Seal(token)}

	if !s.setupDone(ctx) {
		if want := s.cfg.SetupPlexUser; want != "" && !strings.EqualFold(want, acc.Username) && !strings.EqualFold(want, acc.Email) {
			s.log.Warn("setup denied", "plex_user", acc.Username)
			return "", "setup_denied"
		}
		owned := ownedServers(servers)
		if len(owned) == 0 {
			return "", "no_owned_server"
		}
		u, err := s.store.SignIn(ctx, profile, time.Now())
		if err != nil {
			s.log.Error("sign in", "err", err)
			return "", "plex"
		}
		if len(owned) > 1 {
			s.sessions.Put(ctx, sessSetupUserID, u.ID)
			return "/setup/server", ""
		}
		if err := s.claim(ctx, u.ID, owned[0]); err != nil {
			s.log.Error("claim owner", "err", err)
			return "", "setup_denied"
		}
		s.signIn(r, u.ID)
		return "/admin", ""
	}

	serverID, _ := s.store.Setting(ctx, keyServerID)
	res, ok := findServer(servers, serverID)
	if !ok {
		s.log.Info("sign-in refused: no access to server", "plex_user", acc.Username)
		return "", "no_access"
	}
	u, err := s.store.SignIn(ctx, profile, time.Now())
	if err != nil {
		s.log.Error("sign in", "err", err)
		return "", "plex"
	}
	if u.IsOwner && res.Owned {
		// The owner's sign-in refreshes the server token and connections.
		s.saveServer(ctx, res)
	}
	s.signIn(r, u.ID)
	return "/", ""
}

func (s *Server) signIn(r *http.Request, userID int64) {
	ctx := r.Context()
	if err := s.sessions.RenewToken(ctx); err != nil {
		s.log.Error("renew session", "err", err)
	}
	s.sessions.Remove(ctx, sessSetupUserID)
	s.sessions.Put(ctx, sessUserID, userID)
}

func (s *Server) claim(ctx context.Context, userID int64, res plex.Resource) error {
	if err := s.store.ClaimOwner(ctx, userID, serverSettings(s, res)); err != nil {
		return err
	}
	s.ownerKnown.Store(true)
	return nil
}

func (s *Server) saveServer(ctx context.Context, res plex.Resource) {
	for k, v := range serverSettings(s, res) {
		if err := s.store.SetSetting(ctx, k, v); err != nil {
			s.log.Error("save server", "err", err)
		}
	}
}

func serverSettings(s *Server, res plex.Resource) map[string]string {
	conns, _ := json.Marshal(res.Connections)
	return map[string]string{
		keyServerID:        res.ClientIdentifier,
		keyServerName:      res.Name,
		keyServerToken:     s.box.Seal(res.AccessToken),
		keyServerConnsJSON: string(conns),
	}
}

func ownedServers(all []plex.Resource) []plex.Resource {
	var out []plex.Resource
	for _, r := range all {
		if r.Owned {
			out = append(out, r)
		}
	}
	return out
}

func findServer(all []plex.Resource, id string) (plex.Resource, bool) {
	for _, r := range all {
		if id != "" && r.ClientIdentifier == id {
			return r, true
		}
	}
	return plex.Resource{}, false
}

// handleSetupServer lets an owner of several Plex servers pick one.
func (s *Server) handleSetupServer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := s.sessions.GetInt64(ctx, sessSetupUserID)
	if uid == 0 || s.setupDone(ctx) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	u, err := s.store.UserByID(ctx, uid)
	if err != nil {
		http.Redirect(w, r, "/login?e=expired", http.StatusSeeOther)
		return
	}
	token, err := s.box.Open(u.PlexToken)
	if err != nil {
		s.log.Error("open token", "err", err)
		http.Redirect(w, r, "/login?e=expired", http.StatusSeeOther)
		return
	}
	servers, err := s.plex.Servers(ctx, token)
	if err != nil {
		http.Redirect(w, r, "/login?e=plex", http.StatusSeeOther)
		return
	}
	owned := ownedServers(servers)
	if r.Method == http.MethodPost {
		res, ok := findServer(owned, r.PostFormValue("server"))
		if !ok {
			http.Error(w, "unknown server", http.StatusBadRequest)
			return
		}
		if err := s.claim(ctx, u.ID, res); err != nil {
			s.log.Error("claim owner", "err", err)
			http.Redirect(w, r, "/login?e=setup_denied", http.StatusSeeOther)
			return
		}
		s.signIn(r, u.ID)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "setup_server", pageData{Title: "title.setup_server", Data: owned})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Destroy(r.Context()); err != nil {
		s.log.Error("logout", "err", err)
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleLang stores an explicit language choice and returns to the page.
func (s *Server) handleLang(w http.ResponseWriter, r *http.Request) {
	lang := r.PostFormValue("lang")
	if s.i18n.Has(lang) {
		http.SetCookie(w, &http.Cookie{
			Name: i18n.CookieName, Value: lang, Path: "/", MaxAge: 400 * 24 * 3600,
			HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
		})
	}
	back := "/"
	if u, err := url.Parse(r.Referer()); err == nil && u.Host == r.Host && strings.HasPrefix(u.Path, "/") {
		back = u.Path
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
