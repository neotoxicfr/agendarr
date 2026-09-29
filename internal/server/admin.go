package server

import (
	"net/http"
	"strconv"
)

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
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
	s.log.Error("request failed", "path", r.URL.Path, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
