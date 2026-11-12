package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/db"
)

func TestAuthenticationDisabledForLoopbackTests(t *testing.T) {
	handle, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	srv, err := New(app.New(handle))
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.WithAuthentication(false).Handler()

	req := httptest.NewRequest(http.MethodGet, "http://web.test/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "local-test") {
		t.Fatalf("local test session must load dashboard directly: %d %s", rec.Code, rec.Body.String())
	}

	form := url.Values{"name": {"No Login Customer"}}
	req = httptest.NewRequest(http.MethodPost, "http://web.test/customers/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://web.test")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("local test mutation must work without credentials: %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "http://web.test/login", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("login must redirect to app in test mode: %d", rec.Code)
	}
}

func TestAuthenticationRequiredByDefault(t *testing.T) {
	srv, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.authRequired {
		t.Fatal("authentication must default to required")
	}
}
