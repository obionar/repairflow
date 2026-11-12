package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"repairflow/internal/app"
	"repairflow/internal/errs"
	"repairflow/internal/files"
)

func (s *Server) repairList(w http.ResponseWriter, r *http.Request, sess app.Session) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	search, stage := r.URL.Query().Get("search"), r.URL.Query().Get("stage")
	pickedUp, pickedUpFilter := parseBoolFilter(r.URL.Query().Get("picked_up"))
	// Archived repairs are always listed with a badge; there is no archived filter.
	res, err := s.svc.ListRepairs(r.Context(), app.RepairFilter{
		Search: search, Stage: stage, PickedUp: pickedUpFilter,
		Page: page, PerPage: 20, IncludeArchived: true,
	})
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	prev, next := pageBounds(res)
	data := map[string]any{
		"Page": res, "Stages": app.Stages, "Search": search, "Stage": stage,
		"PickedUp": pickedUp, "Prev": prev, "Next": next,
	}
	if r.URL.Query().Get("partial") == "1" {
		s.renderTemplate(w, r, "repairs-results", sess, data)
		return
	}
	s.render(w, r, "tickets", "Repairs", sess, data)
}

// repairCreateForm is the submitted new-repair draft.
type repairCreateForm struct {
	CustomerID                   int64
	CustomerName                 string
	Device, Brand, Model, Serial string
	Issue, Notes                 string
}

func (s *Server) repairNew(w http.ResponseWriter, r *http.Request, sess app.Session) {
	customers, err := s.svc.ListCustomers(r.Context(), app.CustomerFilter{PerPage: 100})
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	selected, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
	selectedName := ""
	for _, customer := range customers.Items {
		if customer.ID == selected {
			selectedName = customer.Name
			break
		}
	}
	s.render(w, r, "ticket_new", "New repair", sess, map[string]any{
		"Customers": customers, "Form": repairCreateForm{CustomerID: selected, CustomerName: selectedName},
	})
}

// resolveCustomer accepts a selected ID or, for the no-JavaScript path, one
// exact customer-name match. It never guesses from a partial or ambiguous name.
func (s *Server) resolveCustomer(ctx context.Context, id int64, name string) (int64, error) {
	if id > 0 {
		return id, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	page, err := s.svc.ListCustomers(ctx, app.CustomerFilter{Search: name, PerPage: 100})
	if err != nil {
		return 0, err
	}
	matches := []int64{}
	for _, customer := range page.Items {
		if strings.EqualFold(strings.TrimSpace(customer.Name), name) {
			matches = append(matches, customer.ID)
		}
	}
	switch len(matches) {
	case 0:
		return 0, errs.New(errs.CodeValidation, "customer %q was not found; choose a suggestion or leave the field blank", name)
	case 1:
		return matches[0], nil
	default:
		return 0, errs.New(errs.CodeConflict, "more than one customer is named %q; choose one from the suggestions", name)
	}
}

func (s *Server) repairCreate(w http.ResponseWriter, r *http.Request, sess app.Session) {
	form := repairCreateForm{
		CustomerID:   parseFormID(r.FormValue("customer_id")),
		CustomerName: r.FormValue("customer_name"),
		Device:       r.FormValue("device"), Brand: r.FormValue("brand"), Model: r.FormValue("model"),
		Serial: r.FormValue("serial"),
		Issue:  r.FormValue("issue"), Notes: r.FormValue("notes"),
	}
	var createErr error
	form.CustomerID, createErr = s.resolveCustomer(r.Context(), form.CustomerID, form.CustomerName)
	var tk app.Repair
	if createErr == nil {
		tk, createErr = s.svc.CreateRepairWithKey(r.Context(), actorOf(sess), app.RepairInput{
			CustomerID: form.CustomerID, DeviceType: form.Device, Brand: form.Brand, Model: form.Model,
			Serial: form.Serial, Issue: form.Issue, Notes: form.Notes,
		}, app.NormalizeIdempotencyKey(r.FormValue("idempotency_key")))
	}
	if createErr != nil {
		if draftable(createErr) {
			customers, cerr := s.svc.ListCustomers(r.Context(), app.CustomerFilter{PerPage: 100})
			if cerr != nil {
				s.renderError(w, r, sessView(sess), cerr, statusOf(cerr))
				return
			}
			s.renderStatus(w, r, "ticket_new", "New repair", sess, createErr.Error(), map[string]any{
				"Customers": customers, "Form": form,
			}, statusOf(createErr))
			return
		}
		s.renderError(w, r, sessView(sess), createErr, statusOf(createErr))
		return
	}
	http.Redirect(w, r, "/repairs/"+itoa(tk.ID), http.StatusSeeOther)
}

type repairForm struct {
	CustomerID                   int64
	CustomerName                 string
	Device, Brand, Model, Serial string
	Issue, Notes, Revision       string
	Draft, Conflict              bool
}

func repairFormOf(t app.Repair) repairForm {
	return repairForm{CustomerID: t.CustomerID, CustomerName: t.CustomerName,
		Device: t.DeviceType, Brand: t.Brand, Model: t.Model, Serial: t.Serial,
		Issue: t.Issue, Notes: t.Notes, Revision: t.Revision}
}

type entryForm struct{ Kind, Body string }

type repairView struct {
	app.RepairDetail
	// Repair carries the record for the templates.
	// R-03 renames the templates and this field goes away with them.
	Repair         app.Repair
	Customers      app.Page[app.Customer]
	Parts          []app.Part
	DeletedParts   []app.Part
	Estimates      []app.Estimate
	Attachs        []app.Attachment
	DeletedAttachs []app.Attachment
	Stages         []string
	EntryKinds     []string
	MaxUpload      int64
	Form           repairForm
	EntryForm      entryForm
	PartForm       partForm
	PartForms      map[int64]partForm
	EstimateForm   estimateForm
	CaptionDrafts  map[int64]string
	DeletedEntries []app.Entry
	EntryRevisions map[int64][]app.EntryRevision
}

func (s *Server) repairViewData(ctx context.Context, id int64) (repairView, error) {
	detail, err := s.svc.GetRepair(ctx, id, true)
	if err != nil {
		return repairView{}, err
	}
	customers, err := s.svc.ListCustomers(ctx, app.CustomerFilter{PerPage: 100})
	if err != nil {
		return repairView{}, err
	}
	active := make([]app.Entry, 0, len(detail.Entries))
	deleted := make([]app.Entry, 0)
	revisions := make(map[int64][]app.EntryRevision)
	for _, entry := range detail.Entries {
		if entry.DeletedAt != "" {
			deleted = append(deleted, entry)
		} else {
			active = append(active, entry)
		}
		revs, err := s.svc.ListEntryRevisions(ctx, entry.ID)
		if err != nil {
			return repairView{}, err
		}
		if len(revs) > 0 {
			revisions[entry.ID] = revs
		}
	}
	detail.Entries = active
	allParts, err := s.svc.ListRepairParts(ctx, id, true)
	if err != nil {
		return repairView{}, err
	}
	parts := make([]app.Part, 0, len(allParts))
	deletedParts := make([]app.Part, 0)
	for _, part := range allParts {
		if part.DeletedAt != "" {
			deletedParts = append(deletedParts, part)
		} else {
			parts = append(parts, part)
		}
	}
	estimates, err := s.svc.ListEstimates(ctx, id)
	if err != nil {
		return repairView{}, err
	}
	allAtts, err := s.svc.ListRepairAttachments(ctx, id, true)
	if err != nil {
		return repairView{}, err
	}
	atts := make([]app.Attachment, 0, len(allAtts))
	deletedAtts := make([]app.Attachment, 0)
	for _, attachment := range allAtts {
		switch attachment.State {
		case "attached":
			atts = append(atts, attachment)
		case "detached":
			deletedAtts = append(deletedAtts, attachment)
		}
	}
	kind := ""
	if len(app.EntryKinds) > 0 {
		kind = app.EntryKinds[0]
	}
	partForms := make(map[int64]partForm, len(parts))
	for _, part := range parts {
		partForms[part.ID] = partFormOf(part)
	}
	return repairView{
		RepairDetail: detail, Repair: detail.Repair,
		Customers: customers,
		Parts:     parts, DeletedParts: deletedParts, Estimates: estimates,
		Attachs: atts, DeletedAttachs: deletedAtts,
		Stages: app.Stages, EntryKinds: app.EntryKinds, MaxUpload: files.MaxAttachmentBytes,
		Form: repairFormOf(detail.Repair), EntryForm: entryForm{Kind: kind},
		PartForm: partForm{Fields: map[string]string{"unit": "pcs"}}, PartForms: partForms,
		CaptionDrafts:  map[int64]string{},
		DeletedEntries: deleted, EntryRevisions: revisions,
	}, nil
}

func (s *Server) repairFormError(w http.ResponseWriter, r *http.Request, sess app.Session, id int64, err error, apply func(*repairView)) {
	if !draftable(err) {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	view, verr := s.repairViewData(r.Context(), id)
	if verr != nil {
		s.renderError(w, r, sessView(sess), verr, statusOf(verr))
		return
	}
	apply(&view)
	s.renderStatus(w, r, "ticket", "Repair "+itoa(id), sess, conflictMessage(err), view, statusOf(err))
}

func (s *Server) repairShow(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	view, err := s.repairViewData(r.Context(), id)
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	s.render(w, r, "ticket", "Repair "+itoa(id), sess, view)
}

func (s *Server) repairUpdate(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	form := repairForm{
		CustomerID: parseFormID(r.FormValue("customer_id")), CustomerName: r.FormValue("customer_name"),
		Device: r.FormValue("device"), Brand: r.FormValue("brand"), Model: r.FormValue("model"),
		Serial: r.FormValue("serial"),
		Issue:  r.FormValue("issue"), Notes: r.FormValue("notes"),
		Revision: r.FormValue("revision"), Draft: true,
	}
	patch := app.RepairPatch{
		DeviceType: &form.Device, Brand: &form.Brand, Model: &form.Model, Serial: &form.Serial,
		Issue: &form.Issue, Notes: &form.Notes,
	}
	if _, present := r.Form["customer_id"]; present {
		form.CustomerID, err = s.resolveCustomer(r.Context(), form.CustomerID, form.CustomerName)
		if err != nil {
			view, verr := s.repairViewData(r.Context(), id)
			if verr != nil {
				s.renderError(w, r, sessView(sess), verr, statusOf(verr))
				return
			}
			view.Form = form
			s.renderStatus(w, r, "ticket", "Repair "+itoa(id), sess, err.Error(), view, statusOf(err))
			return
		}
		patch.CustomerID = &form.CustomerID
	}
	_, err = s.svc.UpdateRepairDetails(r.Context(), actorOf(sess), id, patch, form.Revision)
	if err != nil {
		if draftable(err) {
			view, verr := s.repairViewData(r.Context(), id)
			if verr != nil {
				s.renderError(w, r, sessView(sess), verr, statusOf(verr))
				return
			}
			view.Form = form
			if errs.CodeOf(err) == errs.CodeConflict {
				view.Form.Revision = view.Revision
				view.Form.Conflict = true
			}
			s.renderStatus(w, r, "ticket", "Repair "+itoa(id), sess, conflictMessage(err), view, statusOf(err))
			return
		}
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	redirectBack(w, r, "/repairs/"+itoa(id))
}

func (s *Server) repairArchive(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.repairSetArchived(w, r, sess, true)
}

func (s *Server) repairUnarchive(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.repairSetArchived(w, r, sess, false)
}

func (s *Server) repairSetArchived(w http.ResponseWriter, r *http.Request, sess app.Session, archived bool) {
	id, err := pathID(r, "id")
	if err == nil {
		_, err = s.svc.ArchiveRepair(r.Context(), actorOf(sess), id, archived)
	}
	s.postResult(w, r, sess, err, "/repairs/"+itoa(id))
}

func (s *Server) repairStage(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.repairMutation(w, r, sess, func() error {
		id, err := pathID(r, "id")
		if err != nil {
			return err
		}
		_, err = s.svc.SetRepairStage(r.Context(), actorOf(sess), id, r.FormValue("stage"), r.FormValue("reopen") == "1")
		return err
	})
}

// repairMutation runs one service operation and returns the operator to the
// owning repair on success, or the shared error page on failure.
func (s *Server) repairMutation(w http.ResponseWriter, r *http.Request, sess app.Session, mutate func() error) {
	s.postResult(w, r, sess, mutate(), "/repairs/"+r.PathValue("id"))
}

func (s *Server) repairEntryAdd(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	form := entryForm{Kind: r.FormValue("kind"), Body: r.FormValue("body")}
	_, err = s.svc.AddRepairEntry(r.Context(), actorOf(sess), id, form.Kind, form.Body)
	if err != nil {
		if draftable(err) {
			view, verr := s.repairViewData(r.Context(), id)
			if verr != nil {
				s.renderError(w, r, sessView(sess), verr, statusOf(verr))
				return
			}
			if !entryKindValid(form.Kind) {
				form.Kind = view.EntryForm.Kind
			}
			view.EntryForm = form
			s.renderStatus(w, r, "ticket", "Repair "+itoa(id), sess, conflictMessage(err), view, statusOf(err))
			return
		}
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	redirectBack(w, r, "/repairs/"+itoa(id))
}

func (s *Server) repairEntryEdit(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.repairMutation(w, r, sess, func() error {
		eid, err := pathID(r, "eid")
		if err != nil {
			return err
		}
		body, kind := r.FormValue("body"), r.FormValue("kind")
		_, err = s.svc.EditRepairEntry(r.Context(), actorOf(sess), eid, &kind, &body)
		return err
	})
}

func (s *Server) repairEntryDelete(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.repairMutation(w, r, sess, func() error {
		eid, err := pathID(r, "eid")
		if err != nil {
			return err
		}
		_, err = s.svc.DeleteRepairEntry(r.Context(), actorOf(sess), eid)
		return err
	})
}

func (s *Server) repairEntryRestore(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.repairMutation(w, r, sess, func() error {
		eid, err := pathID(r, "eid")
		if err != nil {
			return err
		}
		_, err = s.svc.RestoreRepairEntry(r.Context(), actorOf(sess), eid)
		return err
	})
}

func (s *Server) repairPickup(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.repairMutation(w, r, sess, func() error {
		id, err := pathID(r, "id")
		if err != nil {
			return err
		}
		_, err = s.svc.SetRepairPickup(r.Context(), actorOf(sess), id,
			r.FormValue("pickup_at"), r.FormValue("picked_up_by"), r.FormValue("pickup_notes"))
		return err
	})
}
