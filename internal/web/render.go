package web

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"repairflow/internal/app"
	"repairflow/internal/errs"
	"repairflow/internal/i18n"
)

type pageView struct {
	Title     string
	Active    string
	Session   *sessionView
	CSRF      string
	Data      any
	Error     string
	Now       string
	Lang      string
	Languages []i18n.Language
	T         func(string, ...any) string
	N         func(string, int, ...any) string
}

// Tr and Pl expose the catalog through methods: html/template can call
// methods with arguments but only call func-valued fields via call.
func (v pageView) Tr(key string, args ...any) string { return v.T(key, args...) }

func (v pageView) Pl(key string, count int, args ...any) string { return v.N(key, count, args...) }

// Act renders an audit action name, falling back to the humanised raw value for
// actions a translation does not cover yet.
func (v pageView) Act(name string) string {
	if s := v.T("action." + name); s != "action."+name {
		return s
	}
	return humanize(name)
}

type sessionView struct {
	Username string
	Role     string
	CanWrite bool
	CanPurge bool
}

func sessView(sess app.Session) *sessionView {
	return &sessionView{Username: sess.Username, Role: sess.Role,
		CanWrite: app.CanWrite(sess.Role), CanPurge: app.CanPurge(sess.Role)}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title string, sess app.Session, data any) {
	s.renderStatus(w, r, name, title, sess, "", data, http.StatusOK)
}

// renderTemplate executes one named fragment (the live-search results) with the
// same page view a full page gets, so its strings and session stay correct.
func (s *Server) renderTemplate(w http.ResponseWriter, r *http.Request, name string, sess app.Session, data any) {
	cat := s.catalogFor(r)
	err := s.tmpl.ExecuteTemplate(w, name, pageView{
		Session: sessView(sess), CSRF: sess.CSRFToken, Data: data,
		Now:  time.Now().UTC().Format("2006-01-02 15:04"),
		Lang: cat.Code, Languages: s.tr.Languages(), T: cat.T, N: cat.N,
	})
	if err != nil {
		s.logError(r, "render_template", err)
		http.Error(w, "internal rendering error", http.StatusInternalServerError)
	}
}

// renderStatus renders a page with an explicit status and an optional flash
// message. It is used to keep submitted drafts on validation/conflict errors
// instead of replacing the page with a generic error.
func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, name, title string, sess app.Session, message string, data any, status int) {
	sessv := sessView(sess)
	cat := s.catalogFor(r)
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	titleText := title
	if cat.Has(title) {
		titleText = cat.T(title)
	}
	err := s.tmpl.ExecuteTemplate(w, name+".html", pageView{
		Title: titleText, Active: activeFor(name), Session: sessv, CSRF: sess.CSRFToken,
		Data: data, Error: message, Now: time.Now().UTC().Format("2006-01-02 15:04"),
		Lang: cat.Code, Languages: s.tr.Languages(), T: cat.T, N: cat.N,
	})
	if err != nil {
		// Template failure is a bug; do not leak internals to the client.
		s.logError(r, "render_template", err)
		http.Error(w, "internal rendering error", http.StatusInternalServerError)
	}
}

// activeFor maps a page template to the navigation section it belongs to.
func activeFor(name string) string {
	switch name {
	case "customers", "customer", "customer_new":
		return "customers"
	case "tickets", "ticket", "ticket_new":
		return "tickets"
	case "settings":
		return "settings"
	default:
		return "dashboard"
	}
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, sess *sessionView, err error, status int) {
	message := err.Error()
	if errs.CodeOf(err) == errs.CodeInternal {
		message = "internal application error"
		s.logError(r, "request", err)
	} else {
		s.logger.Debug("request rejected", "request_id", requestID(r.Context()),
			"error_code", errs.CodeOf(err), "status", status)
	}
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	w.WriteHeader(status)
	cat := s.catalogFor(r)
	view := pageView{Title: cat.T("error.title"), Session: sess, Error: message,
		Lang: cat.Code, Languages: s.tr.Languages(), T: cat.T, N: cat.N}
	if renderErr := s.tmpl.ExecuteTemplate(w, "error.html", view); renderErr != nil {
		s.logError(r, "render_error_page", renderErr)
	}
}

func (s *Server) logError(r *http.Request, operation string, err error) {
	s.logger.Error("operation failed", "request_id", requestID(r.Context()),
		"operation", operation, "error", err)
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "repairflow_session", Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteStrictMode})
}

func (s *Server) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: "repairflow_session", Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: time.Now().Add(app.SessionTTLS),
	})
}

// redirectBack sends the user back to a safe internal page after a mutation.
func redirectBack(w http.ResponseWriter, r *http.Request, fallback string) {
	target := r.Header.Get("Referer")
	if u, err := url.Parse(target); err == nil && u.Host == r.Host && u.Path != "" && u.Path != "/login" {
		http.Redirect(w, r, u.Path, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fallback, http.StatusSeeOther)
}

// catalogFor resolves the request language: browser cookie, then the workspace
// default, then the embedded fallback.
func (s *Server) catalogFor(r *http.Request) *i18n.Catalog {
	if c, err := r.Cookie(languageCookie); err == nil && s.tr.Has(c.Value) {
		return s.tr.For(c.Value)
	}
	if s.language != "" && s.tr.Has(s.language) {
		return s.tr.For(s.language)
	}
	return s.tr.For(s.tr.Default())
}

// pathID parses a numeric path id.
func pathID(r *http.Request, name string) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || v <= 0 {
		return 0, errs.New(errs.CodeValidation, "invalid %s id", name)
	}
	return v, nil
}
