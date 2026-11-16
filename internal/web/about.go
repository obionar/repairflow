package web

import (
	"net/http"

	"repairflow/internal/buildinfo"
)

// about renders the build-identity page. AGPL-3.0 section 13 requires an
// interactive user to be offered the Corresponding Source of the running
// version, so /about must stay reachable without a session.
func (s *Server) about(w http.ResponseWriter, r *http.Request) {
	cat := s.catalogFor(r)
	view := pageView{Title: "About RepairFlow", Data: buildinfo.Get(),
		Lang: cat.Code, Languages: s.tr.Languages(), T: cat.T, N: cat.N}
	// The navigation chrome needs an identity; visitors without a valid
	// session get the bare page.
	if c, err := r.Cookie("repairflow_session"); err == nil {
		if sess, err := s.svc.LookupSession(r.Context(), c.Value); err == nil {
			view.Session = sessView(sess)
			view.CSRF = sess.CSRFToken
		}
	}
	if err := s.tmpl.ExecuteTemplate(w, "about.html", view); err != nil {
		s.logError(r, "render_about", err)
		http.Error(w, "internal rendering error", http.StatusInternalServerError)
	}
}
