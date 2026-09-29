package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/neotoxicfr/agendarr/internal/arr"
	"github.com/neotoxicfr/agendarr/internal/httpx"
	"github.com/neotoxicfr/agendarr/internal/seerr"
	"github.com/neotoxicfr/agendarr/internal/store"
)

var kindNames = map[string]string{store.KindRadarr: "Radarr", store.KindSonarr: "Sonarr", store.KindSeerr: "Seerr"}

type instanceForm struct {
	store.Instance
	HasKey bool
	Errors map[string]string // field -> i18n key
}

func (s *Server) handleAdminInstances(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Instances(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	groups := map[string][]store.Instance{}
	for _, i := range list {
		groups[i.Kind] = append(groups[i.Kind], i)
	}
	s.render(w, r, http.StatusOK, "admin_instances", pageData{Title: "title.admin", Data: map[string]any{
		"Kinds":  []string{store.KindRadarr, store.KindSonarr, store.KindSeerr},
		"Groups": groups,
		"Names":  kindNames,
	}})
}

func (s *Server) handleInstanceForm(w http.ResponseWriter, r *http.Request) {
	f := instanceForm{Instance: store.Instance{Kind: r.URL.Query().Get("kind"), Enabled: true}}
	if id := r.PathValue("id"); id != "" {
		inst, ok := s.instanceFromPath(w, r)
		if !ok {
			return
		}
		f.Instance, f.HasKey = inst, inst.APIKey != ""
	}
	if _, ok := kindNames[f.Kind]; !ok {
		f.Kind = store.KindRadarr
	}
	s.renderInstanceForm(w, r, http.StatusOK, f)
}

func (s *Server) renderInstanceForm(w http.ResponseWriter, r *http.Request, status int, f instanceForm) {
	s.render(w, r, status, "admin_instance_form", pageData{Title: "title.admin", Data: map[string]any{
		"Form": f, "Names": kindNames, "Kinds": []string{store.KindRadarr, store.KindSonarr, store.KindSeerr},
	}})
}

func (s *Server) instanceFromPath(w http.ResponseWriter, r *http.Request) (store.Instance, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return store.Instance{}, false
	}
	inst, err := s.store.Instance(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return inst, false
	}
	if err != nil {
		s.fail(w, r, err)
		return inst, false
	}
	return inst, true
}

// parseInstance reads and validates the form. The API key field left empty
// on an existing instance keeps the stored key.
func (s *Server) parseInstance(r *http.Request, existing *store.Instance) (instanceForm, string) {
	f := instanceForm{Errors: map[string]string{}}
	if existing != nil {
		f.Instance = *existing
		f.HasKey = existing.APIKey != ""
	} else {
		f.Kind = r.PostFormValue("kind")
	}
	f.Name = strings.TrimSpace(r.PostFormValue("name"))
	f.URL = strings.TrimRight(strings.TrimSpace(r.PostFormValue("url")), "/")
	f.Enabled = r.PostFormValue("enabled") != ""
	key := strings.TrimSpace(r.PostFormValue("api_key"))

	if _, ok := kindNames[f.Kind]; !ok {
		f.Kind = store.KindRadarr
		f.Errors["kind"] = "inst.err_kind"
	}
	if f.Name == "" {
		f.Name = kindNames[f.Kind]
	}
	if u, err := url.Parse(f.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		f.Errors["url"] = "inst.err_url"
	}
	plainKey := ""
	switch {
	case key != "":
		plainKey = key
		f.APIKey = s.box.Seal(key)
	case f.HasKey:
		plainKey, _ = s.box.Open(f.APIKey)
	default:
		f.Errors["api_key"] = "inst.err_key"
	}
	return f, plainKey
}

func (s *Server) handleInstanceSave(w http.ResponseWriter, r *http.Request) {
	var existing *store.Instance
	if r.PathValue("id") != "" {
		inst, ok := s.instanceFromPath(w, r)
		if !ok {
			return
		}
		existing = &inst
	}
	f, _ := s.parseInstance(r, existing)
	if len(f.Errors) > 0 {
		s.renderInstanceForm(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	if _, err := s.store.SaveInstance(r.Context(), f.Instance); err != nil {
		s.fail(w, r, err)
		return
	}
	s.instancesChanged()
	s.flash(r, "inst.saved", f.Name)
	http.Redirect(w, r, "/admin/instances", http.StatusSeeOther)
}

func (s *Server) handleInstanceDelete(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instanceFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteInstance(r.Context(), inst.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.instancesChanged()
	s.flash(r, "inst.deleted", inst.Name)
	http.Redirect(w, r, "/admin/instances", http.StatusSeeOther)
}

// handleInstanceTest checks the form values without saving them.
func (s *Server) handleInstanceTest(w http.ResponseWriter, r *http.Request) {
	var existing *store.Instance
	if id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64); err == nil && id > 0 {
		if inst, err := s.store.Instance(r.Context(), id); err == nil {
			existing = &inst
		}
	}
	f, key := s.parseInstance(r, existing)
	T := s.i18n.Map(s.i18n.Pick(r))
	if len(f.Errors) > 0 {
		for _, k := range []string{"url", "api_key", "kind"} {
			if e, ok := f.Errors[k]; ok {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": T[e]})
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	msg, err := testInstance(ctx, f.Kind, f.URL, key)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": describeErr(T, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": fmt.Sprintf(T["inst.test_ok"], msg)})
}

func testInstance(ctx context.Context, kind, baseURL, key string) (string, error) {
	switch kind {
	case store.KindSeerr:
		st, err := seerr.New(baseURL, key).Test(ctx)
		return "Seerr " + st.Version, err
	default:
		st, err := arr.New(baseURL, key).Status(ctx)
		if err == nil && !strings.EqualFold(st.AppName, kind) {
			return "", fmt.Errorf("%w: %s", errWrongApp, st.AppName)
		}
		return st.AppName + " " + st.Version, err
	}
}

var errWrongApp = errors.New("wrong application")

func describeErr(T map[string]string, err error) string {
	switch {
	case errors.Is(err, httpx.ErrUnauthorized):
		return T["inst.test_unauthorized"]
	case errors.Is(err, errWrongApp):
		return fmt.Sprintf(T["inst.test_wrong_app"], strings.TrimPrefix(err.Error(), errWrongApp.Error()+": "))
	default:
		return fmt.Sprintf(T["inst.test_unreachable"], err.Error())
	}
}

// handleSeerrImport creates the Radarr and Sonarr instances Seerr knows about.
// Seerr sees them from its own network: an instance agendarr cannot reach is
// created disabled, for the admin to fix its URL.
func (s *Server) handleSeerrImport(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instanceFromPath(w, r)
	if !ok {
		return
	}
	if inst.Kind != store.KindSeerr {
		http.NotFound(w, r)
		return
	}
	key, err := s.box.Open(inst.APIKey)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	c := seerr.New(inst.URL, key)
	radarrs, err1 := c.Radarrs(ctx)
	sonarrs, err2 := c.Sonarrs(ctx)
	if err := errors.Join(err1, err2); err != nil {
		s.log.Warn("seerr import", "err", err)
		s.flash(r, "inst.import_failed", inst.Name)
		http.Redirect(w, r, "/admin/instances", http.StatusSeeOther)
		return
	}
	existing, err := s.store.Instances(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	known := map[string]bool{}
	for _, e := range existing {
		known[e.Kind+" "+e.URL] = true
	}
	added, disabled := 0, 0
	add := func(kind string, list []seerr.ArrServer) {
		for _, a := range list {
			u := a.URL()
			if known[kind+" "+u] {
				continue
			}
			_, terr := testInstance(ctx, kind, u, a.APIKey)
			ni := store.Instance{Kind: kind, Name: a.Name, URL: u, APIKey: s.box.Seal(a.APIKey), Enabled: terr == nil}
			if ni.Name == "" {
				ni.Name = kindNames[kind]
			}
			if _, err := s.store.SaveInstance(ctx, ni); err != nil {
				s.log.Error("seerr import save", "err", err)
				continue
			}
			added++
			if terr != nil {
				disabled++
			}
		}
	}
	add(store.KindRadarr, radarrs)
	add(store.KindSonarr, sonarrs)
	s.instancesChanged()
	s.flash(r, "inst.imported", strconv.Itoa(added), strconv.Itoa(disabled))
	http.Redirect(w, r, "/admin/instances", http.StatusSeeOther)
}

// instancesChanged is called after any instance change; the calendar cache
// hooks into it.
func (s *Server) instancesChanged() {
	if s.onInstancesChanged != nil {
		s.onInstancesChanged()
	}
}
