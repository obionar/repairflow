package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Bounded in-process login throttling. This complements Argon2id and the
// uniform login error; it is not a distributed rate limiter.
const (
	loginFailureLimit = 5
	loginWindow       = 15 * time.Minute
	loginLockout      = 15 * time.Minute
	maxThrottleKeys   = 4096
)

type throttleEntry struct {
	failures     int
	firstFailure time.Time
	blockedUntil time.Time
}

type loginThrottle struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]throttleEntry
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{now: time.Now, entries: map[string]throttleEntry{}}
}

// retryAfter reports the remaining lockout for a key, or zero when allowed.
func (t *loginThrottle) retryAfter(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.prune(now)
	entry, ok := t.entries[key]
	if !ok || entry.blockedUntil.IsZero() {
		return 0
	}
	if now.Before(entry.blockedUntil) {
		return entry.blockedUntil.Sub(now)
	}
	delete(t.entries, key)
	return 0
}

// failure records one failed attempt and starts a lockout at the limit.
func (t *loginThrottle) failure(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.prune(now)
	entry := t.entries[key]
	if entry.firstFailure.IsZero() || now.Sub(entry.firstFailure) > loginWindow {
		entry = throttleEntry{firstFailure: now}
	}
	entry.failures++
	if entry.failures >= loginFailureLimit {
		entry.blockedUntil = now.Add(loginLockout)
	}
	t.entries[key] = entry
}

// success clears the counter after a valid sign-in.
func (t *loginThrottle) success(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

func (t *loginThrottle) prune(now time.Time) {
	if len(t.entries) > maxThrottleKeys {
		// Bounded failure mode: forgetting counters is safer than unbounded
		// memory growth. Authentication and CSRF checks still apply.
		t.entries = map[string]throttleEntry{}
		return
	}
	for key, entry := range t.entries {
		expired := !entry.blockedUntil.IsZero() && now.After(entry.blockedUntil)
		stale := now.Sub(entry.firstFailure) > loginWindow
		if expired || stale {
			delete(t.entries, key)
		}
	}
}

// loginKey binds attempts to the submitted username and direct client address.
// Proxies are not trusted; the server binds loopback by default.
func loginKey(r *http.Request, username string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return strings.ToLower(strings.TrimSpace(username)) + "|" + host
}
