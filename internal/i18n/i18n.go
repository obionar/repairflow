// Package i18n holds the embedded UI message catalogs and the lookup rules.
//
// Messages are JSON files under catalogs/, one per language.
// The embedded English catalog is the fallback for every key, so a partial
// translation never renders an empty label. Operators can drop
// <workspace>/locales/<lang>.json to override or add a language without
// rebuilding the binary; a malformed override is reported, never fatal.
//
// Placeholders are positional ({0}, {1}) so translators can reorder them.
// Plurals use the CLDR categories one/few/many/other as key suffixes:
// "dashboard.waiting.one", ".few", ".many", ".other".
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed catalogs/*.json
var embedded embed.FS

// Language is one selectable UI language.
type Language struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Catalog is one language's messages.
type Catalog struct {
	Code     string
	Name     string
	messages map[string]string
	// fallback is the source catalog consulted for keys this language does not
	// translate yet (partial catalogs are normal). Nil for the source language.
	fallback *Catalog
}

// Translator resolves a language code to a catalog with English fallback.
type Translator struct {
	catalogs map[string]*Catalog
	def      string
}

type file struct {
	Language string            `json:"language"`
	Name     string            `json:"name"`
	Messages map[string]string `json:"messages"`
}

// New loads the embedded catalogs. English must exist and is the fallback.
func New() (*Translator, error) {
	t := &Translator{catalogs: map[string]*Catalog{}, def: "en"}
	entries, err := embedded.ReadDir("catalogs")
	if err != nil {
		return nil, fmt.Errorf("i18n: cannot list embedded catalogs: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := embedded.ReadFile("catalogs/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("i18n: cannot read %s: %w", entry.Name(), err)
		}
		if err := t.add(raw, entry.Name()); err != nil {
			return nil, err
		}
	}
	source, ok := t.catalogs[t.def]
	if !ok {
		return nil, fmt.Errorf("i18n: embedded %q catalog is missing", t.def)
	}
	for _, c := range t.catalogs {
		if c != source {
			c.fallback = source
		}
	}
	return t, nil
}

// LoadDir merges <dir>/locales/*.json over the embedded catalogs. A missing
// directory is fine; a malformed file is returned as an error so the caller can
// log it and keep running with the embedded set.
func (t *Translator) LoadDir(home string) error {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	dir := filepath.Join(home, "locales")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("i18n: cannot read %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("i18n: cannot read %s: %w", entry.Name(), err)
		}
		if err := t.add(raw, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

// add parses one catalog file and merges it over any existing messages.
func (t *Translator) add(raw []byte, source string) error {
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("i18n: %s is not valid JSON: %w", source, err)
	}
	code := Normalize(f.Language)
	if code == "" {
		return fmt.Errorf("i18n: %s has no language code", source)
	}
	cat, ok := t.catalogs[code]
	if !ok {
		cat = &Catalog{Code: code, messages: map[string]string{}}
		t.catalogs[code] = cat
	}
	if strings.TrimSpace(f.Name) != "" {
		cat.Name = strings.TrimSpace(f.Name)
	}
	for key, value := range f.Messages {
		cat.messages[key] = value
	}
	return nil
}

// Normalize accepts "ru", "ru-RU", "RU_ru" and returns "ru".
func Normalize(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "_", "-")
	if i := strings.IndexByte(code, '-'); i > 0 {
		code = code[:i]
	}
	return code
}

// Default returns the fallback language code.
func (t *Translator) Default() string { return t.def }

// Has reports whether a language is available.
func (t *Translator) Has(code string) bool {
	_, ok := t.catalogs[Normalize(code)]
	return ok
}

// Languages lists the available languages sorted by code.
func (t *Translator) Languages() []Language {
	out := make([]Language, 0, len(t.catalogs))
	for _, c := range t.catalogs {
		out = append(out, Language{Code: c.Code, Name: c.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// For returns the catalog for a code, falling back to the default language.
func (t *Translator) For(code string) *Catalog {
	if c, ok := t.catalogs[Normalize(code)]; ok {
		return c
	}
	return t.catalogs[t.def]
}

// T returns the message for key with positional placeholders replaced.
// A missing key returns the key itself, which is visible in the UI and caught
// by the catalog-parity test rather than rendering an empty label.
func (c *Catalog) T(key string, args ...any) string {
	if msg, ok := c.messages[key]; ok {
		return fill(msg, args)
	}
	if c.fallback != nil {
		if msg, ok := c.fallback.messages[key]; ok {
			return fill(msg, args)
		}
	}
	return key
}

// Has reports whether the catalog contains a key.
func (c *Catalog) Has(key string) bool {
	_, ok := c.messages[key]
	return ok
}

// N returns the plural form of key for n. It tries the CLDR category for the
// language, then "other", then the English form.
func (c *Catalog) N(key string, n int, args ...any) string {
	for _, catalog := range []*Catalog{c, c.fallback} {
		if catalog == nil {
			continue
		}
		for _, suffix := range pluralCandidates(c.Code, n) {
			if msg, ok := catalog.messages[key+"."+suffix]; ok {
				return fill(msg, append([]any{n}, args...))
			}
		}
		if msg, ok := catalog.messages[key]; ok {
			return fill(msg, append([]any{n}, args...))
		}
	}
	return key
}

func fill(msg string, args []any) string {
	for i, arg := range args {
		msg = strings.ReplaceAll(msg, "{"+strconv.Itoa(i)+"}", fmt.Sprint(arg))
	}
	return msg
}

// pluralCandidates returns the CLDR plural categories to try, most specific
// first, for the languages this app ships. Unknown languages use "one/other".
func pluralCandidates(lang string, n int) []string {
	if n < 0 {
		n = -n
	}
	switch lang {
	case "ru":
		switch {
		case n%10 == 1 && n%100 != 11:
			return []string{"one", "other"}
		case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
			return []string{"few", "other"}
		default:
			return []string{"many", "other"}
		}
	case "tr":
		// Turkish has a single plural category.
		return []string{"other"}
	case "fr":
		// French treats 0 and 1 as singular.
		if n <= 1 {
			return []string{"one", "other"}
		}
		return []string{"other", "one"}
	default:
		if n == 1 {
			return []string{"one", "other"}
		}
		return []string{"other", "one"}
	}
}
