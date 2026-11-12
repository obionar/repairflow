package web

import (
	"net/http"

	"repairflow/internal/app"
)

// partForm preserves operator input across validation/conflict errors. Values
// stay as submitted strings so no partial numeric conversion is shown.
type partForm struct {
	ID       int64
	Fields   map[string]string
	Revision string
	Draft    bool
}

func partFormOf(p app.Part) partForm {
	fields := map[string]string{
		"description": p.Description,
		"quantity":    app.FormatQuantityMilli(p.QuantityMilli),
		"unit":        p.Unit,
		"unit_price":  app.FormatCents(p.UnitPriceCents),
		"unit_cost":   "",
	}
	if p.UnitCostCents != nil {
		fields["unit_cost"] = app.FormatCents(*p.UnitCostCents)
	}
	return partForm{ID: p.ID, Fields: fields, Revision: p.Revision}
}

func partFormFromRequest(r *http.Request, id int64) partForm {
	return partForm{ID: id, Revision: r.FormValue("revision"), Draft: true, Fields: map[string]string{
		"description": r.FormValue("description"), "quantity": r.FormValue("quantity"),
		"unit": r.FormValue("unit"), "unit_cost": r.FormValue("unit_cost"),
		"unit_price": r.FormValue("unit_price"),
	}}
}

type estimateForm struct{ Note, Discount string }

// partFields parses the numeric fields shared by add and edit forms. The
// service remains responsible for validation, money arithmetic and revisions.
func partFields(r *http.Request) (qtyMilli, price int64, cost *int64, err error) {
	qtyMilli, err = app.ParseQuantityMilli("quantity", r.FormValue("quantity"))
	if err != nil {
		return 0, 0, nil, err
	}
	price, err = app.ParseCents("unit_price", r.FormValue("unit_price"))
	if err != nil {
		return 0, 0, nil, err
	}
	if raw := r.FormValue("unit_cost"); raw != "" {
		parsed, cerr := app.ParseCents("unit_cost", raw)
		if cerr != nil {
			return 0, 0, nil, cerr
		}
		cost = &parsed
	}
	return qtyMilli, price, cost, nil
}

func (s *Server) repairPartAdd(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.postResult(w, r, sess, err, "/repairs/"+r.PathValue("id"))
		return
	}
	qty, price, cost, err := partFields(r)
	if err == nil {
		_, err = s.svc.AddRepairPart(r.Context(), actorOf(sess), id, app.PartInput{
			Description: r.FormValue("description"), QuantityMilli: qty, Unit: r.FormValue("unit"),
			UnitCostCents: cost, UnitPriceCents: price,
		})
	}
	if err != nil {
		s.repairFormError(w, r, sess, id, err, func(view *repairView) {
			view.PartForm = partFormFromRequest(r, 0)
		})
		return
	}
	redirectBack(w, r, "/repairs/"+itoa(id))
}

func (s *Server) repairPartUpdate(w http.ResponseWriter, r *http.Request, sess app.Session) {
	partID, err := pathID(r, "pid")
	if err != nil {
		s.postResult(w, r, sess, err, "/repairs/"+r.PathValue("id"))
		return
	}
	qty, price, cost, err := partFields(r)
	if err == nil {
		description, unit := r.FormValue("description"), r.FormValue("unit")
		_, err = s.svc.UpdateRepairPart(r.Context(), actorOf(sess), partID, app.PartPatch{
			Description: &description, QuantityMilli: &qty, Unit: &unit,
			UnitCostCents: cost, ClearCost: r.FormValue("unit_cost") == "",
			UnitPriceCents: &price,
		}, r.FormValue("revision"))
	}
	if err != nil {
		repairID := parseFormID(r.PathValue("id"))
		s.repairFormError(w, r, sess, repairID, err, func(view *repairView) {
			view.PartForms[partID] = partFormFromRequest(r, partID)
		})
		return
	}
	redirectBack(w, r, "/repairs/"+r.PathValue("id"))
}

func (s *Server) repairPartRemove(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.repairMutation(w, r, sess, func() error {
		partID, err := pathID(r, "pid")
		if err != nil {
			return err
		}
		_, err = s.svc.DeleteRepairPart(r.Context(), actorOf(sess), partID)
		return err
	})
}

func (s *Server) repairPartRestore(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.repairMutation(w, r, sess, func() error {
		partID, err := pathID(r, "pid")
		if err != nil {
			return err
		}
		_, err = s.svc.RestoreRepairPart(r.Context(), actorOf(sess), partID)
		return err
	})
}

func (s *Server) estimateAccept(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.repairMutation(w, r, sess, func() error {
		eid, err := pathID(r, "eid")
		if err != nil {
			return err
		}
		_, err = s.svc.AcceptEstimate(r.Context(), actorOf(sess), eid)
		return err
	})
}

func (s *Server) estimateCreate(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err == nil {
		in := app.EstimateInput{Note: r.FormValue("note"), DiscountKind: "none"}
		if raw := r.FormValue("discount_percent"); raw != "" {
			pct, perr := app.ParsePercent("discount_percent", raw)
			if perr != nil {
				err = perr
			} else {
				in.DiscountKind, in.DiscountValue = "percent", pct
			}
		}
		if err == nil {
			_, err = s.svc.CreateEstimate(r.Context(), actorOf(sess), id, in)
		}
	}
	if err != nil {
		s.repairFormError(w, r, sess, id, err, func(view *repairView) {
			view.EstimateForm = estimateForm{Note: r.FormValue("note"), Discount: r.FormValue("discount_percent")}
		})
		return
	}
	redirectBack(w, r, "/repairs/"+itoa(id))
}
