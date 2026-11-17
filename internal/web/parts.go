package web

import (
	"net/http"
	"strconv"

	"repairflow/internal/app"
	"repairflow/internal/errs"
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
	quantity := strconv.FormatInt(p.QuantityMilli/1000, 10)
	if p.QuantityMilli%1000 != 0 {
		quantity = app.FormatQuantityMilli(p.QuantityMilli)
	}
	fields := map[string]string{
		"description": p.Description,
		"quantity":    quantity,
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
		"unit_cost": r.FormValue("unit_cost"), "unit_price": r.FormValue("unit_price"),
	}}
}

// partFields parses the numeric fields shared by add and edit forms. The
// service remains responsible for validation, money arithmetic and revisions.
func partFields(r *http.Request) (qtyMilli, price int64, cost *int64, err error) {
	qtyMilli, err = app.ParseQuantityMilli("quantity", r.FormValue("quantity"))
	if err != nil {
		return 0, 0, nil, err
	}
	if qtyMilli%1000 != 0 {
		return 0, 0, nil, errs.New(errs.CodeValidation, "quantity must be a whole number of pieces (got %q)", r.FormValue("quantity"))
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
			Description: r.FormValue("description"), QuantityMilli: qty, Unit: app.UnitPieces,
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
	repairID, err := pathID(r, "id")
	if err != nil {
		s.postResult(w, r, sess, err, "/repairs/"+r.PathValue("id"))
		return
	}
	partID, err := pathID(r, "pid")
	if err == nil {
		err = s.requirePartParent(r, repairID, partID)
	}
	if err != nil {
		s.postResult(w, r, sess, err, "/repairs/"+r.PathValue("id"))
		return
	}
	qty, price, cost, err := partFields(r)
	if err == nil {
		description, unit := r.FormValue("description"), app.UnitPieces
		_, err = s.svc.UpdateRepairPart(r.Context(), actorOf(sess), partID, app.PartPatch{
			Description: &description, QuantityMilli: &qty, Unit: &unit,
			UnitCostCents: cost, ClearCost: r.FormValue("unit_cost") == "",
			UnitPriceCents: &price,
		}, r.FormValue("revision"))
	}
	if err != nil {
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
		repairID, err := pathID(r, "id")
		if err != nil {
			return err
		}
		if err := s.requirePartParent(r, repairID, partID); err != nil {
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
		repairID, err := pathID(r, "id")
		if err != nil {
			return err
		}
		if err := s.requirePartParent(r, repairID, partID); err != nil {
			return err
		}
		_, err = s.svc.RestoreRepairPart(r.Context(), actorOf(sess), partID)
		return err
	})
}
