package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/neotoxicfr/agendarr/internal/config"
	"github.com/neotoxicfr/agendarr/internal/store"
)

func (h *harness) form(c *http.Client, path string, v url.Values) *http.Response {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.http.URL+path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.http.URL)
	res, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

// fakeArr answers as Radarr or Sonarr, and as Seerr listing both.
func fakeArr(t *testing.T) *httptest.Server {
	t.Helper()
	var ts *httptest.Server
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, s string) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, s)
	}
	for _, app := range []string{"radarr", "sonarr"} {
		name := strings.ToUpper(app[:1]) + app[1:]
		mux.HandleFunc("GET /"+app+"/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Api-Key") != app+"-key" {
				w.WriteHeader(401)
				return
			}
			j(w, `{"appName":"`+name+`","version":"5.0"}`)
		})
	}
	mux.HandleFunc("GET /api/v1/settings/radarr", func(w http.ResponseWriter, r *http.Request) {
		u, _ := url.Parse(ts.URL)
		j(w, `[{"name":"Radarr HD","hostname":"`+u.Hostname()+`","port":`+u.Port()+`,"apiKey":"radarr-key","baseUrl":"/radarr"},
		       {"name":"Radarr 4K","hostname":"unreachable.invalid","port":1,"apiKey":"x","is4k":true}]`)
	})
	mux.HandleFunc("GET /api/v1/settings/sonarr", func(w http.ResponseWriter, r *http.Request) {
		u, _ := url.Parse(ts.URL)
		j(w, `[{"name":"","hostname":"`+u.Hostname()+`","port":`+u.Port()+`,"apiKey":"sonarr-key","baseUrl":"/sonarr/"}]`)
	})
	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestInstances(t *testing.T) {
	h := newHarness(t, config0())
	owner, _ := h.signIn("owner")
	arr := fakeArr(t)
	ctx := context.Background()

	// Validation: a bad URL and a missing key re-render the form.
	res := h.form(owner, "/admin/instances/new", url.Values{"kind": {"radarr"}, "url": {"radarr:7878"}})
	res.Body.Close()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid form: %d", res.StatusCode)
	}

	// Test before saving.
	var tr map[string]any
	res = h.form(owner, "/admin/instances/test", url.Values{"kind": {"radarr"}, "url": {arr.URL + "/radarr"}, "api_key": {"radarr-key"}})
	json.NewDecoder(res.Body).Decode(&tr)
	res.Body.Close()
	if tr["ok"] != true {
		t.Fatalf("test: %v", tr)
	}
	// Sonarr's URL declared as Radarr is caught.
	res = h.form(owner, "/admin/instances/test", url.Values{"kind": {"radarr"}, "url": {arr.URL + "/sonarr"}, "api_key": {"sonarr-key"}})
	json.NewDecoder(res.Body).Decode(&tr)
	res.Body.Close()
	if tr["ok"] != false || !strings.Contains(tr["message"].(string), "Sonarr") {
		t.Fatalf("wrong app: %v", tr)
	}

	res = h.form(owner, "/admin/instances/new", url.Values{"kind": {"radarr"}, "url": {arr.URL + "/radarr/"}, "api_key": {"radarr-key"}, "enabled": {"1"}})
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d", res.StatusCode)
	}
	list, _ := h.srv.store.Instances(ctx)
	if len(list) != 1 || list[0].Name != "Radarr" || strings.HasSuffix(list[0].URL, "/") || !list[0].Enabled {
		t.Fatalf("saved: %+v", list)
	}
	if list[0].APIKey == "radarr-key" || strings.Contains(list[0].APIKey, "radarr") {
		t.Fatal("API key stored in clear")
	}
	// Editing with an empty key keeps the key; the stored key passes the test.
	id := itoa(list[0].ID)
	res = h.form(owner, "/admin/instances/"+id, url.Values{"name": {"Radarr HD"}, "url": {arr.URL + "/radarr"}})
	res.Body.Close()
	res = h.form(owner, "/admin/instances/test", url.Values{"id": {id}, "kind": {"radarr"}, "url": {arr.URL + "/radarr"}})
	json.NewDecoder(res.Body).Decode(&tr)
	res.Body.Close()
	if tr["ok"] != true {
		t.Fatalf("stored key test: %v", tr)
	}
	inst, _ := h.srv.store.Instance(ctx, list[0].ID)
	if inst.Name != "Radarr HD" || inst.Enabled {
		t.Fatalf("edit: %+v", inst)
	}

	// Seerr import: skips the Radarr already known, adds Sonarr, and adds
	// the unreachable 4K instance disabled.
	seerrID, err := h.srv.store.SaveInstance(ctx, store.Instance{Kind: store.KindSeerr, Name: "Seerr", URL: arr.URL, APIKey: h.srv.box.Seal("k"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	res = h.form(owner, "/admin/instances/"+itoa(seerrID)+"/import", nil)
	res.Body.Close()
	list, _ = h.srv.store.Instances(ctx)
	got := map[string]bool{}
	for _, i := range list {
		got[i.Kind+" "+i.Name] = i.Enabled
	}
	if len(list) != 4 || got["radarr Radarr 4K"] || !got["sonarr Sonarr"] {
		t.Fatalf("import: %+v", got)
	}

	// A plain user cannot reach the instance pages.
	friend, _ := h.signIn("friend")
	if code := h.status(friend, "/admin/instances"); code != http.StatusForbidden {
		t.Fatalf("friend: %d", code)
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func config0() config.Config { return config.Config{} }

func TestDescribeErr(t *testing.T) {
	T := map[string]string{"inst.test_no_host": "nohost %s", "inst.test_refused": "refused", "inst.test_not_api": "notapi",
		"inst.test_unreachable": "other %s", "inst.test_timeout": "timeout"}
	ctx := context.Background()
	cases := map[string]string{
		"http://seer.invalid:5055": "nohost seer.invalid",
		"http://127.0.0.1:1":       "refused",
	}
	for u, want := range cases {
		_, err := testInstance(ctx, "seerr", u, "k")
		if got := describeErr(T, err); got != want {
			t.Errorf("%s: got %q want %q (%v)", u, got, want, err)
		}
	}
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html>login</html>")
	}))
	defer html.Close()
	_, err := testInstance(ctx, "radarr", html.URL, "k")
	if got := describeErr(T, err); got != "notapi" {
		t.Errorf("html page: %q", got)
	}
}
