package web

import (
	"net/http"
	"strconv"
	"time"

	"repairflow/internal/app"
	"repairflow/internal/errs"
	"repairflow/internal/i18n"
)

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if !s.authRequired {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.publicView(w, r, "login.title", "")
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.authRequired {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := parseRequestForm(r); err != nil {
		s.renderError(w, r, nil, errs.Wrap(errs.CodeValidation, err, "invalid login form"), statusOf(err))
		return
	}
	username := r.FormValue("username")
	key := loginKey(r, username)
	if wait := s.loginLimit.retryAfter(key); wait > 0 {
		seconds := int((wait + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		s.renderError(w, r, nil, errs.New(errs.CodePermission,
			"too many failed sign-in attempts; try again later"), http.StatusTooManyRequests)
		return
	}
	_, token, err := s.svc.Login(r.Context(), username, r.FormValue("password"))
	if err != nil {
		if errs.CodeOf(err) == errs.CodeInternal {
			s.renderError(w, r, nil, err, http.StatusInternalServerError)
			return
		}
		s.loginLimit.failure(key)
		w.WriteHeader(http.StatusUnauthorized)
		s.publicView(w, r, "login.title", err.Error())
		return
	}
	s.loginLimit.success(key)
	s.setSession(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, sess app.Session) {
	if !s.authRequired {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if c, err := r.Cookie("repairflow_session"); err == nil {
		if err := s.svc.Logout(r.Context(), c.Value); err != nil {
			s.renderError(w, r, sessView(sess), err, statusOf(err))
			return
		}
	}
	s.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, sess app.Session) {
	customers, err := s.svc.ListCustomers(r.Context(), app.CustomerFilter{Page: 1, PerPage: 5})
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	queues, err := s.svc.Dashboard(r.Context())
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	s.render(w, r, "dashboard", "Dashboard", sess, map[string]any{
		"Customers": customers,
		"Queues":    queues,
	})
}

// publicView renders the login page, which has no session and therefore no
// navigation chrome.
func (s *Server) publicView(w http.ResponseWriter, r *http.Request, titleKey, message string) {
	cat := s.catalogFor(r)
	view := pageView{Title: cat.T(titleKey), Error: message, Lang: cat.Code,
		Languages: s.tr.Languages(), T: cat.T, N: cat.N}
	if err := s.tmpl.ExecuteTemplate(w, "login.html", view); err != nil {
		s.logError(r, "render_login", err)
	}
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.render(w, r, "settings", "settings.title", sess, nil)
}

// settingsLanguage stores the browser language override. The workspace
// default lives in config.toml and is not changed here.
func (s *Server) settingsLanguage(w http.ResponseWriter, r *http.Request, sess app.Session) {
	lang := i18n.Normalize(r.FormValue("language"))
	if !s.tr.Has(lang) {
		s.renderError(w, r, sessView(sess),
			errs.New(errs.CodeValidation, "unknown interface language %q", r.FormValue("language")),
			http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: languageCookie, Value: lang, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 60 * 60,
	})
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) settingsPassword(w http.ResponseWriter, r *http.Request, sess app.Session) {
	newPassword := r.FormValue("new_password")
	if newPassword != r.FormValue("confirm_password") {
		s.renderError(w, r, sessView(sess), errs.New(errs.CodeValidation, "new passwords do not match"), http.StatusBadRequest)
		return
	}
	err := s.svc.ChangePassword(r.Context(), actorOf(sess), sess.Username,
		r.FormValue("current_password"), newPassword)
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	s.clearSession(w) // password change ends all sessions; sign in again
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
