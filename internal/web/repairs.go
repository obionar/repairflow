package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	s.render(w, r, "tickets", "repairs.title", sess, data)
}

// repairCreateForm is the submitted new-repair draft.
type repairCreateForm struct {
	CustomerID                   int64
	CustomerName                 string
	Device, Brand, Model, Serial string
	Issue                        string
	IDKey                        string
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
	if selected > 0 && selectedName == "" {
		// The requested customer may be older than the first suggestion page.
		// Load it by ID so a direct link still pre-fills the real record.
		customer, err := s.svc.GetCustomer(r.Context(), selected)
		if err != nil {
			s.renderError(w, r, sessView(sess), err, statusOf(err))
			return
		}
		selectedName = customer.Name
		customers.Items = append(customers.Items, customer)
	}
	s.render(w, r, "ticket_new", "repair_new.title", sess, map[string]any{
		"Customers": customers, "Form": repairCreateForm{CustomerID: selected, CustomerName: selectedName, IDKey: newIdempotencyKey()},
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
	matches := []int64{}
	for page := 1; ; page++ {
		res, err := s.svc.ListCustomers(ctx, app.CustomerFilter{Search: name, Page: page, PerPage: 100})
		if err != nil {
			return 0, err
		}
		for _, customer := range res.Items {
			if strings.EqualFold(strings.TrimSpace(customer.Name), name) {
				matches = append(matches, customer.ID)
				if len(matches) > 1 {
					return 0, errs.New(errs.CodeValidation, "more than one customer is named %q; choose one from the suggestions", name)
				}
			}
		}
		if page*res.PerPage >= res.Total || len(res.Items) == 0 {
			break
		}
	}
	switch len(matches) {
	case 0:
		return 0, errs.New(errs.CodeValidation, "customer %q was not found; choose a suggestion or leave the field blank", name)
	case 1:
		return matches[0], nil
	default:
		return 0, errs.New(errs.CodeValidation, "more than one customer is named %q; choose one from the suggestions", name)
	}
}

func (s *Server) repairCreate(w http.ResponseWriter, r *http.Request, sess app.Session) {
	form := repairCreateForm{
		CustomerID:   parseFormID(r.FormValue("customer_id")),
		CustomerName: r.FormValue("customer_name"),
		Device:       r.FormValue("device"), Brand: r.FormValue("brand"), Model: r.FormValue("model"),
		Serial: r.FormValue("serial"),
		Issue:  r.FormValue("issue"),
		IDKey:  r.FormValue("idempotency_key"),
	}
	var createErr error
	form.CustomerID, createErr = s.resolveCustomer(r.Context(), form.CustomerID, form.CustomerName)
	var tk app.Repair
	if createErr == nil {
		tk, createErr = s.svc.CreateRepairWithKey(r.Context(), actorOf(sess), app.RepairInput{
			CustomerID: form.CustomerID, DeviceType: form.Device, Brand: form.Brand, Model: form.Model,
			Serial: form.Serial, Issue: form.Issue,
		}, app.NormalizeIdempotencyKey(form.IDKey))
	}
	if createErr != nil {
		if draftable(createErr) {
			customers, cerr := s.svc.ListCustomers(r.Context(), app.CustomerFilter{PerPage: 100})
			if cerr != nil {
				s.renderError(w, r, sessView(sess), cerr, statusOf(cerr))
				return
			}
			s.renderStatus(w, r, "ticket_new", "repair_new.title", sess, s.flashText(r, createErr), map[string]any{
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
	Issue, Revision              string
	Draft, Conflict              bool
}

func repairFormOf(t app.Repair) repairForm {
	return repairForm{CustomerID: t.CustomerID, CustomerName: t.CustomerName,
		Device: t.DeviceType, Brand: t.Brand, Model: t.Model, Serial: t.Serial,
		Issue: t.Issue, Revision: t.Revision}
}

type entryForm struct{ Kind, Body string }
type pickupForm struct{ By, Notes string }

type repairView struct {
	app.RepairDetail
	// Repair carries the record for the templates.
	// R-03 renames the templates and this field goes away with them.
	Repair         app.Repair
	Customers      app.Page[app.Customer]
	Parts          []app.Part
	DeletedParts   []app.Part
	Attachs        []app.Attachment
	DeletedAttachs []app.Attachment
	Stages         []string
	EntryKinds     []string
	MaxUpload      int64
	Form           repairForm
	EntryForm      entryForm
	EntryDrafts    map[int64]*entryForm
	PickupForm     pickupForm
	PartForm       partForm
	PartForms      map[int64]partForm
	CaptionDrafts  map[int64]string
	DeletedEntries []app.Entry
	EntryRevisions map[int64][]app.EntryRevision
	// LabelDriver is the configured label printer, empty when printing is off.
	LabelDriver string
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
	for _, entry := range detail.Entries {
		if entry.DeletedAt != "" {
			deleted = append(deleted, entry)
		} else {
			active = append(active, entry)
		}
	}
	revisions, err := s.svc.ListRepairEntryRevisions(ctx, id)
	if err != nil {
		return repairView{}, err
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
	labelPrinter, err := s.svc.LabelPrinter(ctx)
	if err != nil {
		return repairView{}, err
	}
	return repairView{
		RepairDetail: detail, Repair: detail.Repair,
		Customers: customers,
		Parts:     parts, DeletedParts: deletedParts,
		Attachs: atts, DeletedAttachs: deletedAtts,
		Stages: app.Stages, EntryKinds: app.EntryKinds, MaxUpload: files.MaxAttachmentBytes,
		Form: repairFormOf(detail.Repair), EntryForm: entryForm{Kind: kind},
		PartForm: partForm{Fields: map[string]string{}}, PartForms: partForms,
		CaptionDrafts:  map[int64]string{},
		DeletedEntries: deleted, EntryRevisions: revisions,
		LabelDriver: labelPrinter.Driver,
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
	s.renderStatus(w, r, "ticket", s.pageTitle(r, "repair.title", id), sess, s.flashText(r, err), view, statusOf(err))
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
	if r.URL.Query().Get("label") == "printed" {
		s.renderNotice(w, r, "ticket", s.pageTitle(r, "repair.title", id), sess,
			s.pageTitle(r, "repair.label_printed"), view)
		return
	}
	s.render(w, r, "ticket", s.pageTitle(r, "repair.title", id), sess, view)
}

// repairLabel prints the intake sticker: the repair number and customer name,
// so a device on the shelf can be matched back to this page.
func (s *Server) repairLabel(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	if _, err := s.svc.PrintRepairLabel(r.Context(), actorOf(sess), id); err != nil {
		view, verr := s.repairViewData(r.Context(), id)
		if verr != nil {
			s.renderError(w, r, sessView(sess), verr, statusOf(verr))
			return
		}
		// The printer message is the operator's instruction ("check the
		// printer before printing again"), so it is shown as written.
		s.renderStatus(w, r, "ticket", s.pageTitle(r, "repair.title", id), sess,
			err.Error(), view, statusOf(err))
		return
	}
	// Redirect, so a refresh shows the page instead of printing a second label.
	http.Redirect(w, r, "/repairs/"+itoa(id)+"?label=printed", http.StatusSeeOther)
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
		Serial:   r.FormValue("serial"),
		Issue:    r.FormValue("issue"),
		Revision: r.FormValue("revision"), Draft: true,
	}
	patch := app.RepairPatch{
		DeviceType: &form.Device, Brand: &form.Brand, Model: &form.Model, Serial: &form.Serial,
		Issue: &form.Issue,
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
			s.renderStatus(w, r, "ticket", s.pageTitle(r, "repair.title", id), sess, s.flashText(r, err), view, statusOf(err))
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
			s.renderStatus(w, r, "ticket", s.pageTitle(r, "repair.title", id), sess, s.flashText(r, err), view, statusOf(err))
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
			s.renderStatus(w, r, "ticket", s.pageTitle(r, "repair.title", id), sess, s.flashText(r, err), view, statusOf(err))
			return
		}
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	redirectBack(w, r, "/repairs/"+itoa(id))
}

func (s *Server) repairEntryEdit(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	eid, err := pathID(r, "eid")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	form := entryForm{Kind: r.FormValue("kind"), Body: r.FormValue("body")}
	_, err = s.svc.EditRepairEntry(r.Context(), actorOf(sess), eid, &form.Kind, &form.Body)
	if err != nil {
		s.repairFormError(w, r, sess, id, err, func(view *repairView) {
			if !entryKindValid(form.Kind) {
				for _, entry := range append(view.Entries, view.DeletedEntries...) {
					if entry.ID == eid {
						form.Kind = entry.Kind
						break
					}
				}
			}
			view.EntryDrafts = map[int64]*entryForm{eid: &form}
		})
		return
	}
	redirectBack(w, r, "/repairs/"+itoa(id))
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
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	form := pickupForm{By: r.FormValue("picked_up_by"), Notes: r.FormValue("pickup_notes")}
	// Collection time is the moment the operator records it; no backdating control.
	_, err = s.svc.SetRepairPickup(r.Context(), actorOf(sess), id,
		time.Now().UTC().Format(time.RFC3339), form.By, form.Notes)
	if err != nil {
		s.repairFormError(w, r, sess, id, err, func(view *repairView) { view.PickupForm = form })
		return
	}
	redirectBack(w, r, "/repairs/"+itoa(id))
}
