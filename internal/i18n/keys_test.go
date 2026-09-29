package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neotoxicfr/agendarr/web"
)

// A key missing from a locale renders as an empty label with no error at all.
// These tests tie the templates, the Go code and the locales together.

var (
	tmplKeyRe  = regexp.MustCompile(`index\s+\$?\.T\s+"([^"]+)"`)
	titleKeyRe = regexp.MustCompile(`Title:\s+"([^"]+)"`)
	errCodeRe  = regexp.MustCompile(`"([a-z_]+)":\s*true`)
	jsKeyRe    = regexp.MustCompile(`\bt\("([^"]+)"\)`)
)

func usedKeys(t *testing.T) map[string]string {
	t.Helper()
	keys := map[string]string{}
	add := func(re *regexp.Regexp, file, prefix string) {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if _, ok := keys[prefix+m[1]]; !ok {
				keys[prefix+m[1]] = file
			}
		}
	}
	walk := func(root, ext string, fn func(string)) {
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(p, ext) && !strings.HasSuffix(p, "_test.go") {
				fn(p)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	walk("../../web/templates", ".html", func(p string) { add(tmplKeyRe, p, "") })
	walk("../../web/static/js", ".js", func(p string) { add(jsKeyRe, p, "") })
	walk("../server", ".go", func(p string) { add(titleKeyRe, p, "") })
	add(errCodeRe, "../server/auth.go", "err.")
	return keys
}

func TestTemplateKeysExistInEveryLocale(t *testing.T) {
	c, err := Load(web.FS, "locales")
	if err != nil {
		t.Fatal(err)
	}
	raw := rawLocales(t)
	keys := usedKeys(t)
	if len(keys) < 20 {
		t.Fatalf("only %d keys found: the scanners are broken", len(keys))
	}
	for key, file := range keys {
		for _, lang := range c.Langs() {
			if _, ok := raw[lang][key]; !ok {
				t.Errorf("%s: key %q missing from %s.json", file, key, lang)
			}
		}
	}
}

func TestLocalesAtParity(t *testing.T) {
	raw := rawLocales(t)
	for lang, m := range raw {
		for k := range raw[Fallback] {
			if _, ok := m[k]; !ok {
				t.Errorf("%s.json lacks %q", lang, k)
			}
		}
		for k := range m {
			if _, ok := raw[Fallback][k]; !ok {
				t.Errorf("%s.json has %q, unknown to %s.json", lang, k, Fallback)
			}
		}
	}
}

// rawLocales reads the locale files without the fallback merge done by Load.
func rawLocales(t *testing.T) map[string]map[string]string {
	t.Helper()
	files, _ := filepath.Glob("../../web/locales/*.json")
	sort.Strings(files)
	out := map[string]map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m, err := decode(b)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out[strings.TrimSuffix(filepath.Base(f), ".json")] = m
	}
	return out
}
