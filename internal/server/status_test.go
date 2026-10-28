package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neotoxicfr/agendarr/internal/config"
	"github.com/neotoxicfr/agendarr/internal/store"
)

// The status is closed without a key, asks for it, and sums up the week.
func TestStatus(t *testing.T) {
	today := time.Now().Format(time.DateOnly)
	soon := time.Now().AddDate(0, 0, 3).Format(time.DateOnly)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"id":1,"title":"Dune","digitalRelease":"`+today+`T00:00:00Z","monitored":true,"hasFile":true},
			{"id":2,"title":"Arrival","digitalRelease":"`+soon+`T00:00:00Z","monitored":true}]`)
	}))
	defer up.Close()
	key := strings.Repeat("k", 32) // a fake key, long enough

	get := func(h *harness, hdr, val string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodGet, h.http.URL+"/api/status", nil)
		if hdr != "" {
			req.Header.Set(hdr, val)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}

	if code, _ := get(newHarness(t, config.Config{DataDir: t.TempDir()}), "Authorization", "Bearer "+key); code != http.StatusNotFound {
		t.Fatalf("no key configured: %d", code)
	}
	h := newHarness(t, config.Config{DataDir: t.TempDir(), StatusToken: key})
	if _, err := h.srv.store.SaveInstance(context.Background(), store.Instance{Kind: "radarr", Name: "Radarr", URL: up.URL,
		APIKey: h.srv.box.Seal("k"), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	h.srv.instancesChanged()
	if code, _ := get(h, "Authorization", "Bearer "+strings.Repeat("x", 32)); code != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", code)
	}
	code, out := get(h, "X-Api-Key", key)
	if code != http.StatusOK || out["today"] != 1.0 || out["available_today"] != 1.0 || out["week"] != 2.0 {
		t.Fatalf("status: %d %v", code, out)
	}
	if next, _ := out["next"].([]any); len(next) != 1 || next[0] != soon+" Arrival" {
		t.Fatalf("next: %v", out["next"])
	}
	if code, _ := get(h, "Authorization", "Bearer "+key); code != http.StatusOK {
		t.Fatalf("bearer: %d", code)
	}
}
