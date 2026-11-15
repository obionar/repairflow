package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A burst of login requests must not start unbounded Argon2 work. Once the
// small worker budget is full, the extra request is refused before hashing.
func TestLoginRefusesWhenPasswordWorkIsSaturated(t *testing.T) {
	srv, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.loginWork = make(chan struct{}, 1)
	srv.loginWork <- struct{}{}

	form := "username=someone&password=secret"
	req := httptest.NewRequest(http.MethodPost, "http://web.test/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.loginSubmit(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("saturated login work: status %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("saturated login response must tell the client when to retry")
	}
}
