package i18n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T) *Translator {
	t.Helper()
	tr, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func TestEmbeddedCatalogsAndParity(t *testing.T) {
	tr := load(t)
	for _, code := range []string{"en", "el", "ru"} {
		if !tr.Has(code) {
			t.Fatalf("missing embedded language %q", code)
		}
	}
	en := tr.For("en")
	// Languages the project keeps complete. Others (es/fr/de) may be partial:
	// a missing key falls back to English, which is normal for a new
	// translation, but every language must keep a complete plural fallback.
	complete := map[string]bool{"en": true, "el": true, "ru": true, "tr": true}
	for _, lang := range tr.Languages() {
		cat := tr.For(lang.Code)
		for key := range cat.messages {
			if en.Has(key) {
				continue
			}
			if base, ok := pluralBase(key); ok && en.Has(base+".other") {
				continue // extra plural category the language needs
			}
			t.Errorf("%s defines key %q that English does not have", lang.Code, key)
		}
		if !complete[lang.Code] {
			continue
		}
		if lang.Code == "en" {
			continue
		}
		for key := range en.messages {
			if cat.Has(key) {
				continue
			}
			if base, ok := pluralBase(key); ok && cat.Has(base+".other") {
				continue
			}
			t.Errorf("%s is missing key %q", lang.Code, key)
		}
		for key := range en.messages {
			if base, ok := pluralBase(key); ok && !cat.Has(base+".other") {
				t.Errorf("%s is missing plural fallback %q", lang.Code, base+".other")
			}
		}
	}
	if got := len(tr.Languages()); got < 4 {
		t.Fatalf("languages = %d, want at least 4", got)
	}
	for i := 1; i < len(tr.Languages()); i++ {
		if tr.Languages()[i-1].Code >= tr.Languages()[i].Code {
			t.Fatalf("languages must be sorted by code: %+v", tr.Languages())
		}
	}
}

func TestFallbackAndFormatting(t *testing.T) {
	tr := load(t)
	if got := tr.For("xx").Code; got != "en" {
		t.Fatalf("unknown language fell back to %q, want en", got)
	}
	if got := tr.For("ru-RU").Code; got != "ru" {
		t.Fatalf("ru-RU normalized to %q, want ru", got)
	}
	if got := tr.For("el").T("settings.signed_in", "alice"); !strings.Contains(got, "alice") {
		t.Fatalf("placeholder not replaced: %q", got)
	}
	if got := tr.For("en").T("nope.missing"); got != "nope.missing" {
		t.Fatalf("missing key = %q, want the key itself", got)
	}
}

func TestPluralRules(t *testing.T) {
	tr := load(t)
	ru := tr.For("ru")
	cases := []struct {
		n    int
		want string
	}{
		{1, "1 ожидает"},
		{2, "2 ожидают"},
		{4, "4 ожидают"},
		{5, "5 ожидают"},
		{11, "11 ожидают"},
		{21, "21 ожидает"},
		{22, "22 ожидают"},
		{0, "0 ожидают"},
	}
	for _, tc := range cases {
		if got := ru.N("dashboard.waiting", tc.n); got != tc.want {
			t.Errorf("ru N(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
	if got := tr.For("en").N("dashboard.open", 1); got != "1 open" {
		t.Errorf("en N(1) = %q", got)
	}
	if got := tr.For("en").N("dashboard.open", 3); got != "3 open" {
		t.Errorf("en N(3) = %q", got)
	}
}

// pluralBase reports whether key is a plural variant and returns its base.
func pluralBase(key string) (string, bool) {
	for _, suffix := range []string{".one", ".few", ".many", ".other"} {
		if strings.HasSuffix(key, suffix) {
			return strings.TrimSuffix(key, suffix), true
		}
	}
	return "", false
}

func TestPartialCatalogFallsBackPerKey(t *testing.T) {
	tr := load(t)
	de := tr.For("de")
	// German is allowed to be partial: a missing key renders English, never the
	// raw key name.
	if got := de.T("customers.search"); got != tr.For("en").T("customers.search") {
		t.Fatalf("partial catalog fallback = %q", got)
	}
	if got := de.T("nope.missing"); got != "nope.missing" {
		t.Fatalf("unknown key = %q, want the key itself", got)
	}
}

func TestPluralRulesTurkishAndFrench(t *testing.T) {
	tr := load(t)
	if got := tr.For("tr").N("dashboard.waiting", 5); got != "5 bekliyor" {
		t.Errorf("tr N(5) = %q", got)
	}
	if got := tr.For("fr").N("dashboard.open", 0); got != "0 ouverte" {
		t.Errorf("fr N(0) = %q, want the singular form", got)
	}
	if got := tr.For("fr").N("dashboard.open", 3); got != "3 ouvertes" {
		t.Errorf("fr N(3) = %q", got)
	}
}

func TestWorkspaceOverrideAndMalformedFile(t *testing.T) {
	tr := load(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "locales"), 0o700); err != nil {
		t.Fatal(err)
	}
	override := `{"language":"ru","name":"Русский (local)","messages":{"nav.dashboard":"Панель (локально)"}}`
	if err := os.WriteFile(filepath.Join(home, "locales", "ru.json"), []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tr.LoadDir(home); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if got := tr.For("ru").T("nav.dashboard"); got != "Панель (локально)" {
		t.Errorf("override not applied: %q", got)
	}
	// Unrelated keys survive the merge.
	if got := tr.For("ru").T("nav.customers"); got != "Клиенты" {
		t.Errorf("merge dropped existing keys: %q", got)
	}

	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, "locales"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "locales", "xx.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tr.LoadDir(bad); err == nil {
		t.Fatal("malformed override must return an error")
	}
	// A missing locales directory is not an error.
	if err := tr.LoadDir(t.TempDir()); err != nil {
		t.Fatalf("missing locales dir: %v", err)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"ru": "ru", "RU": "ru", "ru-RU": "ru", "ru_RU": "ru", " el ": "el", "": "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
