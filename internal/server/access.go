package server

import (
	"context"
	"errors"
	"github.com/neotoxicfr/agendarr/internal/jellyfin"
	"github.com/neotoxicfr/agendarr/internal/store"
	"time"

	"github.com/neotoxicfr/agendarr/internal/plex"
)

// accessCheckEvery is how often a user's access to the Plex server is
// re-verified; a user removed from the Plex share is signed out at the next
// check.
const accessCheckEvery = 24 * time.Hour

// RunAccessChecks re-verifies users' Plex access until ctx is done.
func (s *Server) RunAccessChecks(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.checkAccess(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) checkAccess(ctx context.Context, now time.Time) {
	if !s.setupDone(ctx) {
		return
	}
	serverID, err := s.store.Setting(ctx, keyServerID)
	if err != nil || serverID == "" {
		return
	}
	users, err := s.store.UsersToCheck(ctx, now.Add(-accessCheckEvery))
	if err != nil {
		s.log.Error("access check", "err", err)
		return
	}
	if s.isJellyfin(ctx) {
		s.checkJellyfinAccess(ctx, users, now)
		return
	}
	for _, u := range users {
		token, err := s.box.Open(u.Token)
		if err != nil || token == "" {
			continue
		}
		servers, err := s.plex.Servers(ctx, token)
		var hasAccess bool
		switch {
		case errors.Is(err, plex.ErrUnauthorized):
			// The user revoked agendarr in their Plex devices, or the
			// token expired: they have to sign in again.
			hasAccess = false
		case err != nil:
			// plex.tv unreachable: never lock people out on a network error.
			s.log.Warn("access check", "user", u.Username, "err", err)
			continue
		default:
			_, hasAccess = findServer(servers, serverID)
		}
		if u.IsOwner {
			hasAccess = true
		}
		if !hasAccess {
			s.log.Info("access revoked", "user", u.Username)
		}
		if err := s.store.MarkChecked(ctx, u.ID, hasAccess, now); err != nil {
			s.log.Error("mark checked", "err", err)
		}
	}
}

// checkJellyfinAccess reads the server's accounts once: a deleted or
// disabled account loses access. Any error leaves everyone as they are.
func (s *Server) checkJellyfinAccess(ctx context.Context, users []store.User, now time.Time) {
	jf, err := s.jellyfinClient(ctx)
	if err != nil || jf == nil {
		s.log.Warn("access check", "err", err)
		return
	}
	accounts, err := jf.Users(ctx)
	if err != nil {
		s.log.Warn("access check", "err", err)
		return
	}
	enabled := map[string]bool{}
	for _, a := range accounts {
		enabled[jellyfin.FormatID(a.ID)] = !a.Policy.IsDisabled
	}
	for _, u := range users {
		hasAccess := u.IsOwner || enabled[u.AccountID]
		if !hasAccess {
			s.log.Info("access revoked", "user", u.Username)
		}
		if err := s.store.MarkChecked(ctx, u.ID, hasAccess, now); err != nil {
			s.log.Error("mark checked", "err", err)
		}
	}
}
