package web

import (
	"net/http"
	"strconv"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

// customerForm is the submitted customer draft. It is used both for normal
// rendering and for validation/conflict re-renders so no operator input is
// discarded.
type customerForm struct {
	Name, Phone, Email, Address, Notes, Revision string
	Draft, Conflict                              bool
}

func customerFormOf(c app.Customer) customerForm {
	return customerForm{Name: c.Name, Phone: c.Phone, Email: c.Email, Address: c.Address, Notes: c.Notes, Revision: c.Revision}
}

func (s *Server) customerList(w http.ResponseWriter, r *http.Request, sess app.Session) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	search := r.URL.Query().Get("search")
	// Archived records are always listed and marked with a badge: the list has no
	// archived filter, archiving only takes a record out of the dashboard queues.
	res, err := s.svc.ListCustomers(r.Context(), app.CustomerFilter{Search: search, Page: page, PerPage: 20, IncludeArchived: true})
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	prev, next := pageBounds(res)
	data := map[string]any{"Page": res, "Search": search, "Prev": prev, "Next": next}
	if r.URL.Query().Get("partial") == "1" {
		s.renderTemplate(w, r, "customers-results", sess, data)
		return
	}
	s.render(w, r, "customers", "customers.title", sess, data)
}

func (s *Server) customerNew(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.render(w, r, "customer_new", "customer_new.title", sess, map[string]any{"Form": customerForm{}})
}

func (s *Server) customerCreate(w http.ResponseWriter, r *http.Request, sess app.Session) {
	form := customerForm{
		Name: r.FormValue("name"), Phone: r.FormValue("phone"),
		Email: r.FormValue("email"), Address: r.FormValue("address"), Notes: r.FormValue("notes"), Draft: true,
	}
	c, err := s.svc.CreateCustomer(r.Context(), actorOf(sess), app.CustomerInput{
		Name: form.Name, Phone: form.Phone, Email: form.Email, Address: form.Address, Notes: form.Notes,
	})
	if err != nil {
		if draftable(err) {
			s.renderStatus(w, r, "customer_new", "customer_new.title", sess, s.flashText(r, err),
				map[string]any{"Form": form}, statusOf(err))
			return
		}
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	// Land on the created record, not back on the empty form.
	http.Redirect(w, r, "/customers/"+itoa(c.ID), http.StatusSeeOther)
}

func (s *Server) customerShow(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	c, err := s.svc.GetCustomer(r.Context(), id)
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	history, err := s.svc.CustomerHistory(r.Context(), id)
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	repairs, err := s.svc.ListRepairs(r.Context(), app.RepairFilter{CustomerID: id, PerPage: 50})
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	s.render(w, r, "customer", s.pageTitle(r, "customer.title", c.Name), sess, map[string]any{
		"Customer": c, "History": history, "Repairs": repairs, "Form": customerFormOf(c),
	})
}

func (s *Server) customerArchive(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.customerSetArchived(w, r, sess, true)
}

func (s *Server) customerUnarchive(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.customerSetArchived(w, r, sess, false)
}

func (s *Server) customerSetArchived(w http.ResponseWriter, r *http.Request, sess app.Session, archived bool) {
	id, err := pathID(r, "id")
	if err == nil {
		_, err = s.svc.ArchiveCustomer(r.Context(), actorOf(sess), id, archived)
	}
	s.postResult(w, r, sess, err, "/customers/"+itoa(id))
}

func (s *Server) customerUpdate(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	form := customerForm{
		Name: r.FormValue("name"), Phone: r.FormValue("phone"),
		Email: r.FormValue("email"), Address: r.FormValue("address"), Notes: r.FormValue("notes"),
		Revision: r.FormValue("revision"), Draft: true,
	}
	_, err = s.svc.UpdateCustomer(r.Context(), actorOf(sess), id, app.CustomerPatch{
		Name: &form.Name, Phone: &form.Phone, Email: &form.Email, Address: &form.Address, Notes: &form.Notes,
	}, form.Revision)
	if err != nil {
		if draftable(err) {
			c, gerr := s.svc.GetCustomer(r.Context(), id)
			if gerr != nil {
				s.renderError(w, r, sessView(sess), gerr, statusOf(gerr))
				return
			}
			history, herr := s.svc.CustomerHistory(r.Context(), id)
			if herr != nil {
				s.renderError(w, r, sessView(sess), herr, statusOf(herr))
				return
			}
			repairs, terr := s.svc.ListRepairs(r.Context(), app.RepairFilter{CustomerID: id, PerPage: 50})
			if terr != nil {
				s.renderError(w, r, sessView(sess), terr, statusOf(terr))
				return
			}
			if errs.CodeOf(err) == errs.CodeConflict {
				form.Revision = c.Revision
				form.Conflict = true
			}
			s.renderStatus(w, r, "customer", s.pageTitle(r, "customer.title", c.Name), sess, s.flashText(r, err), map[string]any{
				"Customer": c, "History": history, "Repairs": repairs, "Form": form,
			}, statusOf(err))
			return
		}
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	redirectBack(w, r, "/customers")
}
