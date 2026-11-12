package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginThrottleLocksAndResets(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	throttle := newLoginThrottle()
	throttle.now = func() time.Time { return now }

	for i := 0; i < loginFailureLimit; i++ {
		if wait := throttle.retryAfter("user|127.0.0.1"); wait != 0 {
			t.Fatalf("attempt %d must be allowed, wait=%v", i+1, wait)
		}
		throttle.failure("user|127.0.0.1")
	}
	if wait := throttle.retryAfter("user|127.0.0.1"); wait != loginLockout {
		t.Fatalf("locked key: wait=%v", wait)
	}
	if wait := throttle.retryAfter("other|127.0.0.1"); wait != 0 {
		t.Fatalf("different username must not be blocked: %v", wait)
	}
	now = now.Add(loginLockout)
	if wait := throttle.retryAfter("user|127.0.0.1"); wait != 0 {
		t.Fatalf("lockout must expire: %v", wait)
	}
	throttle.failure("user|127.0.0.1")
	throttle.success("user|127.0.0.1")
	if wait := throttle.retryAfter("user|127.0.0.1"); wait != 0 {
		t.Fatalf("success must reset: %v", wait)
	}
}

func TestLoginThrottleWindowExpires(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	throttle := newLoginThrottle()
	throttle.now = func() time.Time { return now }
	for i := 0; i < loginFailureLimit-1; i++ {
		throttle.failure("user|::1")
	}
	now = now.Add(loginWindow + time.Minute)
	throttle.failure("user|::1")
	if wait := throttle.retryAfter("user|::1"); wait != 0 {
		t.Fatalf("stale failures must not accumulate across the window: %v", wait)
	}
}

func TestLoopbackHostGuard(t *testing.T) {
	srv, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.WithLoopbackOnly(true).Handler()
	for host, want := range map[string]int{
		"evil.test":      http.StatusForbidden,
		"127.0.0.1:8091": http.StatusSeeOther,
		"localhost:8091": http.StatusSeeOther,
		"[::1]:8091":     http.StatusSeeOther,
	} {
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("host %q: status %d want %d", host, rec.Code, want)
		}
	}
}
