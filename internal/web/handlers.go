package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

// draftable reports whether an operation error should return the operator to
// the same form with the draft intact (validation/conflict), rather than the
// generic rejection page.
func draftable(err error) bool {
	switch errs.CodeOf(err) {
	case errs.CodeValidation, errs.CodeConflict, errs.CodeUsage:
		return true
	}
	return false
}

// conflictMessage keeps raw revision hashes out of the operator-facing page.
func conflictMessage(err error) string {
	if errs.CodeOf(err) == errs.CodeConflict {
		return "This record changed while you were editing. Compare the saved values below, then save again to apply your draft."
	}
	return err.Error()
}

func (s *Server) postResult(w http.ResponseWriter, r *http.Request, sess app.Session, err error, fallback string) {
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	redirectBack(w, r, fallback)
}

func actorOf(sess app.Session) string { return "web:" + sess.Username }

// attachmentHome returns the owning repair page for post-mutation redirects
// (the Referrer-Policy suppresses Referer, so we cannot rely on it).
func attachmentHome(a app.Attachment) string {
	if a.RepairID > 0 {
		return "/repairs/" + itoa(a.RepairID)
	}
	return "/"
}

func statusOf(err error) int {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	switch errs.CodeOf(err) {
	case errs.CodeNotFound:
		return http.StatusNotFound
	case errs.CodePermission:
		return http.StatusForbidden
	case errs.CodeConflict:
		return http.StatusConflict
	case errs.CodeValidation, errs.CodeUsage:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func parseBoolFilter(raw string) (string, *bool) {
	switch raw {
	case "yes":
		value := true
		return raw, &value
	case "no":
		value := false
		return raw, &value
	default:
		return "", nil
	}
}

func parseFormID(raw string) int64 {
	id, _ := strconv.ParseInt(raw, 10, 64)
	return id
}

func entryKindValid(kind string) bool {
	for _, valid := range app.EntryKinds {
		if kind == valid {
			return true
		}
	}
	return false
}

// pageBounds reports the previous/next page numbers for a listing, using 0
// to mean "no such page".
func pageBounds[T any](p app.Page[T]) (prev, next int) {
	if p.Page > 1 {
		prev = p.Page - 1
	}
	if p.PerPage > 0 && p.Page*p.PerPage < p.Total {
		next = p.Page + 1
	}
	return prev, next
}

// repairRedirect sends a legacy /tickets… request to its /repairs… equivalent.
// A permanent redirect keeps the method and body, so old forms still submit.
func repairRedirect(w http.ResponseWriter, r *http.Request) {
	target := "/repairs" + strings.TrimPrefix(r.URL.Path, "/tickets")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}
