package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"repairflow/internal/buildinfo"
)

// The build description exposes the Corresponding Source without a config or
// schema change, so a modified deployment can point at its own repository.
func TestBuildInfoExposesSourceURL(t *testing.T) {
	raw, err := json.Marshal(buildinfo.Get())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["source_url"] != buildinfo.SourceURL || buildinfo.SourceURL == "" {
		t.Fatalf("source_url = %v, want %q", got["source_url"], buildinfo.SourceURL)
	}
}

// /about must be public: AGPL-3.0 section 13 requires the source offer to
// reach every user, signed in or not.
func TestAboutPageWithoutSession(t *testing.T) {
	f := newWeb(t)
	body := f.get("/about", nil)
	for _, want := range []string{
		"RepairFlow", "AGPL-3.0-only",
		buildinfo.Version, buildinfo.Commit, buildinfo.Get().Platform,
		`href="` + buildinfo.SourceURL + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("about page missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, f.home) {
		t.Fatalf("about page leaked the workspace path %q:\n%s", f.home, body)
	}

	login := f.get("/login", nil)
	if !strings.Contains(login, `href="/about"`) {
		t.Fatal("login page does not link to /about")
	}
}

func TestAboutPageWithSession(t *testing.T) {
	f := newWeb(t)
	cookie := f.login("admin", "password12345")

	body := f.get("/about", cookie)
	for _, want := range []string{buildinfo.Version, buildinfo.SourceURL} {
		if !strings.Contains(body, want) {
			t.Fatalf("authenticated about page missing %q:\n%s", want, body)
		}
	}

	dashboard := f.get("/", cookie)
	if !strings.Contains(dashboard, `href="/about"`) {
		t.Fatal("navigation does not link to /about")
	}
}

func TestAboutRejectsOtherMethods(t *testing.T) {
	f := newWeb(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "http://web.test/about", nil)
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /about: status %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}
