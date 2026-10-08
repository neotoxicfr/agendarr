package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// handleAdmin opens on the instances until at least one exists, since that
// is the first thing a fresh install needs.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/instances", http.StatusSeeOther)
}

// flash stores a one-shot message: an i18n key and its arguments.
func (s *Server) flash(r *http.Request, key string, args ...string) {
	s.sessions.Put(r.Context(), "flash", append([]string{key}, args...))
}

func (s *Server) takeFlash(r *http.Request, T map[string]string) string {
	v, ok := s.sessions.Pop(r.Context(), "flash").([]string)
	if !ok || len(v) == 0 {
		return ""
	}
	args := make([]any, len(v)-1)
	for i, a := range v[1:] {
		args[i] = a
	}
	return fmt.Sprintf(T[v[0]], args...)
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.Users(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	server, _ := s.store.Setting(r.Context(), keyServerName)
	s.render(w, r, http.StatusOK, "admin_users", pageData{Title: "title.admin", Data: map[string]any{
		"Users": users, "Server": server,
	}})
}

func (s *Server) handleAdminUserAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if id == currentUser(r).ID {
		http.Error(w, "you cannot change your own account", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	switch r.PathValue("action") {
	case "promote":
		err = s.store.SetAdmin(ctx, id, true)
	case "demote":
		err = s.store.SetAdmin(ctx, id, false)
	case "delete":
		err = s.store.DeleteUser(ctx, id)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	path := r.URL.Path
	if strings.HasPrefix(path, "/ics/") {
		path = "/ics/…" // the feed token is a credential: keep it out of logs
	}
	s.log.Error("request failed", "path", path, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
