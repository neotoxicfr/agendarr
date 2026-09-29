// Package i18n loads the UI translations and picks a language per request.
//
// Templates read labels with {{index .T "key"}}. A missing key renders as an
// empty string without any error, so keys_test.go checks that every key used
// by a template or by Go code exists in every locale.
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
)

// Fallback is the language used when nothing better matches, and the one
// every other locale falls back to key by key.
const Fallback = "fr"

// CookieName stores an explicit language choice.
const CookieName = "agendarr_lang"

// Catalog holds every loaded locale. It is immutable after Load.
type Catalog struct {
	langs []string
	m     map[string]map[string]string
}

// Load reads every <lang>.json from dir in fsys.
func Load(fsys fs.FS, dir string) (*Catalog, error) {
	files, err := fs.Glob(fsys, path.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	c := &Catalog{m: map[string]map[string]string{}}
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		m, err := decode(b)
		if err != nil {
			return nil, fmt.Errorf("i18n: %s: %w", f, err)
		}
		lang := strings.TrimSuffix(path.Base(f), ".json")
		c.m[lang] = m
		c.langs = append(c.langs, lang)
	}
	if len(c.m[Fallback]) == 0 {
		return nil, fmt.Errorf("i18n: fallback locale %q missing or empty", Fallback)
	}
	sort.Strings(c.langs)
	// Merge the fallback under every locale so a missing key degrades to
	// French rather than to an empty label.
	for lang, m := range c.m {
		if lang == Fallback {
			continue
		}
		for k, v := range c.m[Fallback] {
			if _, ok := m[k]; !ok {
				m[k] = v
			}
		}
	}
	return c, nil
}

// Langs lists the available languages.
func (c *Catalog) Langs() []string { return c.langs }

// Map returns the labels of lang (templates must not mutate it).
func (c *Catalog) Map(lang string) map[string]string {
	if m, ok := c.m[lang]; ok {
		return m
	}
	return c.m[Fallback]
}

// T returns one label, or the key itself when unknown.
func (c *Catalog) T(lang, key string) string {
	if v, ok := c.Map(lang)[key]; ok {
		return v
	}
	return key
}

// Has reports whether lang is available.
func (c *Catalog) Has(lang string) bool {
	_, ok := c.m[lang]
	return ok
}

// Pick chooses the language for a request: explicit cookie, then
// Accept-Language (in the order sent; q-values are rarely meaningful), then
// the fallback.
func (c *Catalog) Pick(r *http.Request) string {
	if ck, err := r.Cookie(CookieName); err == nil && c.Has(ck.Value) {
		return ck.Value
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		base := strings.ToLower(strings.SplitN(tag, "-", 2)[0])
		if c.Has(base) {
			return base
		}
	}
	return Fallback
}

func decode(b []byte) (map[string]string, error) {
	var m map[string]string
	err := json.Unmarshal(b, &m)
	return m, err
}
