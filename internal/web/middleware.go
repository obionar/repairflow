package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"repairflow/internal/app"
	"repairflow/internal/config"
	"repairflow/internal/errs"
)

type requestIDKey struct{}

var fallbackRequestID atomic.Uint64

func newRequestID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("fallback-%x-%x", time.Now().UnixNano(), fallbackRequestID.Add(1))
}

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

// Unwrap preserves access to the underlying writer for ResponseController.
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := newRequestID()
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		w.Header().Set("X-Request-ID", id)
		tracked := &responseWriter{ResponseWriter: w}
		h := tracked.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		if !s.applyRequestBodyLimit(tracked, r) {
			// The request was refused before any form parsing or authentication work.
		} else if s.loopbackOnly && !config.IsLoopbackHost(r.Host) {
			s.renderError(tracked, r, nil, errs.New(errs.CodePermission, "request host is not allowed"), http.StatusForbidden)
		} else if r.Method == http.MethodPost && !sameOrigin(r) {
			s.renderError(tracked, r, nil, errs.New(errs.CodeValidation, "request origin does not match the host"), http.StatusForbidden)
		} else {
			next.ServeHTTP(tracked, r)
		}
		status := tracked.status
		if status == 0 {
			status = http.StatusOK
		}
		s.logger.Debug("http request", "request_id", id, "method", r.Method,
			"path", r.URL.Path, "status", status, "duration_ms", time.Since(started).Milliseconds())
	})
}

// sameOrigin validates the request origin (CSRF layer 1).
//
// Browser reality (found by the E2E suite): Chromium omits `Origin` on
// same-origin form POSTs and our own no-referrer policy suppresses `Referer`,
// so matching those headers alone rejects legitimate users. The reliable
// signal is `Sec-Fetch-Site`. Rules:
//   - Sec-Fetch-Site: same-origin -> trusted browser navigation;
//   - an explicit Origin (or Referer) must match the Host — "null" is hostile;
//   - no browser metadata at all (curl, agents) -> defer to the CSRF token
//     check in authed(), which remains the primary defense.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site == "same-origin" {
		return true
	}
	check := func(raw string) bool {
		if raw == "" || raw == "null" {
			return false
		}
		u, err := url.Parse(raw)
		return err == nil && strings.EqualFold(u.Host, r.Host)
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		return check(origin)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return check(ref)
	}
	return true
}

// authed wraps a handler with session authentication and a minimum role.
func (s *Server) authed(next func(w http.ResponseWriter, r *http.Request, sess app.Session), minRole string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authRequired {
			// Explicit loopback-only test/demo mode; no identity is authenticated.
			next(w, r, app.Session{Username: "local-test", Role: app.RoleAdmin})
			return
		}
		cookie, err := r.Cookie("repairflow_session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		sess, err := s.svc.LookupSession(r.Context(), cookie.Value)
		if err != nil {
			s.clearSession(w)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !roleAllows(sess.Role, minRole) {
			s.renderError(w, r, sessView(sess), errs.New(errs.CodePermission,
				"role %q may not perform this action", sess.Role), http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			if err := parseRequestForm(r); err != nil {
				s.renderError(w, r, sessView(sess), errs.Wrap(errs.CodeValidation, err,
					"invalid form body"), statusOf(err))
				return
			}
			if !app.SecureTokenEqual(r.FormValue("csrf_token"), sess.CSRFToken) {
				s.renderError(w, r, sessView(sess), errs.New(errs.CodeValidation,
					"invalid or missing CSRF token; reload the page and retry"), http.StatusForbidden)
				return
			}
		}
		next(w, r, sess)
	}
}

func roleAllows(role, min string) bool {
	switch min {
	case app.RoleViewer:
		return true
	case app.RoleTechnician:
		return app.CanWrite(role)
	case app.RoleAdmin:
		return app.CanManageUsers(role)
	}
	return false
}
