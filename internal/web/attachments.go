package web

import (
	"io"
	"net/http"
	"path/filepath"
	"strconv"

	"repairflow/internal/app"
	"repairflow/internal/errs"
	"repairflow/internal/files"
)

func (s *Server) attachmentAdd(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.postResult(w, r, sess, err, "/repairs/"+r.PathValue("id"))
		return
	}
	if err := r.ParseMultipartForm(files.MaxAttachmentBytes + (1 << 20)); err != nil {
		s.postResult(w, r, sess, errs.Wrap(errs.CodeValidation, err, "upload too large or malformed"), "/repairs/"+itoa(id))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.postResult(w, r, sess, errs.Wrap(errs.CodeValidation, err, "file field is required"), "/repairs/"+itoa(id))
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			s.logError(r, "close_upload", err)
		}
	}()
	s.logger.Debug("attachment upload", "request_id", requestID(r.Context()), "repair_id", id)
	entryID, _ := strconv.ParseInt(r.FormValue("entry_id"), 10, 64)
	var reader io.Reader = file
	_, err = s.svc.AddAttachment(r.Context(), actorOf(sess), id, entryID,
		filepath.Base(header.Filename), reader, r.FormValue("caption"), "web")
	s.postResult(w, r, sess, err, "/repairs/"+itoa(id))
}

func (s *Server) attachmentDownload(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	reader, meta, err := s.svc.OpenBytes(r.Context(), id)
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	defer func() {
		if err := reader.Close(); err != nil {
			s.logError(r, "close_attachment_download", err)
		}
	}()
	// Attachment-only disposition: uploaded content is never rendered inline
	// unless it is a validated preview-safe image within the preview size bound
	// (preview allowlist). Original bytes are always served unchanged.
	if meta.PreviewInline {
		w.Header().Set("Content-Type", meta.MediaType)
		w.Header().Set("Content-Disposition", `inline; filename="`+meta.DisplayName+`"`)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+meta.DisplayName+`"`)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Content-derived ETag: the gallery re-requests the same originals on every
	// page view, so let the browser revalidate instead of re-downloading. The
	// cache is browser-private and disposable; it never alters stored bytes.
	w.Header().Set("ETag", `"`+meta.SHA256+`"`)
	w.Header().Set("Cache-Control", "private, max-age=300")
	if r.Header.Get("If-None-Match") == `"`+meta.SHA256+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if _, err := io.Copy(w, reader); err != nil {
		// Headers/bytes may already be sent; record the failure without trying
		// to replace the response or exposing internals to the client.
		s.logError(r, "attachment_download", err)
	}
}

// attachmentMutation preserves the existing fallback: a missing attachment
// returns to the dashboard; a successful mutation returns to its repair.
func (s *Server) attachmentMutation(w http.ResponseWriter, r *http.Request, sess app.Session, mutate func(int64) (app.Attachment, error)) {
	id, err := pathID(r, "id")
	var a app.Attachment
	if err == nil {
		a, err = mutate(id)
	}
	s.postResult(w, r, sess, err, attachmentHome(a))
}

func (s *Server) attachmentLink(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.attachmentMutation(w, r, sess, func(id int64) (app.Attachment, error) {
		return s.svc.LinkEntry(r.Context(), actorOf(sess), id, parseFormID(r.FormValue("entry_id")))
	})
}

func (s *Server) attachmentUnlink(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.attachmentMutation(w, r, sess, func(id int64) (app.Attachment, error) {
		return s.svc.UnlinkEntry(r.Context(), actorOf(sess), id, parseFormID(r.FormValue("entry_id")))
	})
}

func (s *Server) attachmentCaption(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	a, err := s.svc.GetAttachment(r.Context(), id)
	if err != nil {
		s.renderError(w, r, sessView(sess), err, statusOf(err))
		return
	}
	_, err = s.svc.SetCaption(r.Context(), actorOf(sess), id, r.FormValue("caption"))
	if err != nil {
		s.repairFormError(w, r, sess, a.RepairID, err, func(view *repairView) {
			view.CaptionDrafts[id] = r.FormValue("caption")
		})
		return
	}
	redirectBack(w, r, attachmentHome(a))
}

func (s *Server) attachmentDetach(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.attachmentMutation(w, r, sess, func(id int64) (app.Attachment, error) {
		return s.svc.Detach(r.Context(), actorOf(sess), id)
	})
}

func (s *Server) attachmentRestore(w http.ResponseWriter, r *http.Request, sess app.Session) {
	s.attachmentMutation(w, r, sess, func(id int64) (app.Attachment, error) {
		return s.svc.Restore(r.Context(), actorOf(sess), id)
	})
}

func (s *Server) attachmentPurge(w http.ResponseWriter, r *http.Request, sess app.Session) {
	id, err := pathID(r, "id")
	var a app.Attachment
	if err == nil {
		if !app.CanPurge(sess.Role) {
			err = errs.New(errs.CodePermission, "only administrators may purge attachments")
		} else {
			a, err = s.svc.Purge(r.Context(), actorOf(sess), id, r.FormValue("confirm") == "1")
		}
	}
	s.postResult(w, r, sess, err, attachmentHome(a))
}
