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
	if _, exists := t.entries[key]; !exists {
		// Make room for a new key without ever evicting an active lockout.
		// If every entry is currently locked, leave the map untouched and do
		// not track this new key; the existing protections remain in force.
		for len(t.entries) >= maxThrottleKeys {
			oldest := t.oldestEvictable(now)
			if oldest == "" {
				return
			}
			delete(t.entries, oldest)
		}
	}
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

// oldestEvictable returns the oldest non-locked entry. Active lockouts are
// never candidates, so capacity pressure cannot cancel a real lockout.
func (t *loginThrottle) oldestEvictable(now time.Time) string {
	var oldestKey string
	var oldest time.Time
	for key, entry := range t.entries {
		if !entry.blockedUntil.IsZero() && now.Before(entry.blockedUntil) {
			continue
		}
		if oldestKey == "" || entry.firstFailure.Before(oldest) {
			oldestKey, oldest = key, entry.firstFailure
		}
	}
	return oldestKey
}

func (t *loginThrottle) prune(now time.Time) {
	for key, entry := range t.entries {
		locked := !entry.blockedUntil.IsZero() && now.Before(entry.blockedUntil)
		expired := !entry.blockedUntil.IsZero() && !now.Before(entry.blockedUntil)
		// A lockout created late in the failure window gets its own full
		// duration; the older first-failure timestamp cannot prune it early.
		stale := !locked && now.Sub(entry.firstFailure) > loginWindow
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
