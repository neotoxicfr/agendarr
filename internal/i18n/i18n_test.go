package i18n

import (
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestPickAndFallback(t *testing.T) {
	c, err := Load(fstest.MapFS{
		"l/fr.json": {Data: []byte(`{"a":"A-fr","b":"B-fr"}`)},
		"l/en.json": {Data: []byte(`{"a":"A-en"}`)},
	}, "l")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.T("en", "b"); got != "B-fr" {
		t.Fatalf("fallback: %q", got)
	}
	if got := c.T("de", "a"); got != "A-fr" {
		t.Fatalf("unknown lang: %q", got)
	}
	cases := []struct{ cookie, accept, want string }{
		{"", "", "fr"},
		{"", "en-US,en;q=0.9", "en"},
		{"", "de-DE, en;q=0.5", "en"},
		{"fr", "en-US", "fr"},
		{"xx", "en", "en"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Language", tc.accept)
		if tc.cookie != "" {
			r.Header.Set("Cookie", CookieName+"="+tc.cookie)
		}
		if got := c.Pick(r); got != tc.want {
			t.Errorf("cookie=%q accept=%q: got %q want %q", tc.cookie, tc.accept, got, tc.want)
		}
	}
	if _, err := Load(fstest.MapFS{"l/en.json": {Data: []byte(`{}`)}}, "l"); err == nil {
		t.Fatal("missing fallback accepted")
	}
}
