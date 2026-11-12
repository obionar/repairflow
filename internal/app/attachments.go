package app

import (
	"context"
	"io"
	"time"

	"repairflow/internal/errs"
	"repairflow/internal/files"
)

// PurgeGrace is the minimum retention after detach before permanent purge.
const PurgeGrace = 30 * 24 * time.Hour

// PreviewMediaTypes is the strict inline-preview allowlist. Everything
// else is download-only.
var PreviewMediaTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// PreviewMaxBytes bounds inline image rendering. v0.1 has no thumbnail
// cache, so the gallery would otherwise pull full originals into every page
// view. Larger preview-safe images keep their original bytes and stay
// downloadable; they are simply not rendered inline. 8 MiB covers typical
// phone photos.
const PreviewMaxBytes int64 = 8 << 20

// Attachment is one stored repair-related file with its metadata.
type Attachment struct {
	ID            int64   `json:"id"`
	RepairID      int64   `json:"repair_id"`
	EntryIDs      []int64 `json:"entry_ids"`
	DisplayName   string  `json:"display_name"`
	MediaType     string  `json:"media_type"`
	PreviewSafe   bool    `json:"preview_safe"`
	PreviewInline bool    `json:"preview_inline"`
	SizeBytes     int64   `json:"size_bytes"`
	SHA256        string  `json:"sha256"`
	Caption       string  `json:"caption"`
	Source        string  `json:"source"`
	State         string  `json:"state"` // attached | detached | purged
	DetachedAt    string  `json:"detached_at,omitempty"`
	PurgeAfter    string  `json:"purge_after,omitempty"`
	CreatedBy     string  `json:"created_by"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`

	storageName string // opaque on-disk name; never serialized
}

const attachmentCols = "id, repair_id, display_name, storage_name, media_type, size_bytes, sha256, caption, source, state, detached_at, purge_after, created_by, created_at, updated_at"

// setPreview derives the inline-preview flags from the sniffed media type and
// the byte size. preview_safe keeps its original meaning (validated image
// type); preview_inline additionally requires the bounded size.
func setPreview(a *Attachment) {
	a.PreviewSafe = PreviewMediaTypes[a.MediaType]
	a.PreviewInline = a.PreviewSafe && a.SizeBytes <= PreviewMaxBytes
}

// WithStore binds the workspace file store used for byte operations.
func (s *Service) WithStore(st *files.Store) *Service {
	s.store = st
	return s
}

// SetClock replaces the service clock (tests only; e.g. retention timing).
func (s *Service) SetClock(now func() time.Time) { s.now = func() time.Time { return now().UTC() } }

func (s *Service) requireStore() (*files.Store, error) {
	if s.store == nil {
		return nil, errs.New(errs.CodeInternal, "attachment file store is not configured")
	}
	return s.store, nil
}

// AddAttachment streams one file into the workspace and records it on a
// repair, optionally linked to a log entry of the SAME repair. The
// display name is metadata only; bytes are hash-verified on arrival.
func (s *Service) AddAttachment(ctx context.Context, actor string, repairID, entryID int64, displayName string, src io.Reader, caption, source string) (Attachment, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Attachment{}, err
	}
	store, err := s.requireStore()
	if err != nil {
		return Attachment{}, err
	}
	displayName = files.SafeDisplayName(displayName)
	if displayName == "" || len(displayName) > 300 {
		return Attachment{}, errs.New(errs.CodeValidation, "display name must be 1-300 characters of a plain file name")
	}
	caption, err = validateText("caption", caption, 1000, false)
	if err != nil {
		return Attachment{}, err
	}
	source, err = validateText("source", source, 50, true)
	if err != nil {
		return Attachment{}, err
	}

	if _, err := s.GetRepairRow(ctx, repairID); err != nil {
		return Attachment{}, err
	}
	if entryID > 0 {
		e, err := s.getEntry(ctx, entryID)
		if err != nil {
			return Attachment{}, err
		}
		if e.RepairID != repairID {
			return Attachment{}, errs.New(errs.CodeValidation, "entry %d belongs to repair %d, not %d", entryID, e.RepairID, repairID)
		}
	}

	stored, err := store.Add(repairID, src, files.MaxAttachmentBytes)
	if err != nil {
		return Attachment{}, err
	}
	ts := s.timestamp()
	var out Attachment
	err = s.InTx(ctx, func(ctx context.Context) error {
		scanErr := s.q(ctx).QueryRowContext(ctx,
			`INSERT INTO attachments(repair_id, display_name, storage_name, media_type, size_bytes, sha256, caption, source, created_by, created_at, updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?) RETURNING `+attachmentCols,
			repairID, displayName, stored.StorageName, stored.MediaType, stored.SizeBytes, stored.SHA256, caption, source, actor, ts, ts).
			Scan(&out.ID, &out.RepairID, &out.DisplayName, &out.storageName, &out.MediaType, &out.SizeBytes, &out.SHA256,
				&out.Caption, &out.Source, &out.State, &out.DetachedAt, &out.PurgeAfter, &out.CreatedBy, &out.CreatedAt, &out.UpdatedAt)
		if scanErr != nil {
			return errs.Wrap(errs.CodeInternal, scanErr, "cannot record attachment")
		}
		if entryID > 0 {
			if _, err := s.q(ctx).ExecContext(ctx,
				`INSERT INTO attachment_links(attachment_id, entry_id, created_at) VALUES(?,?,?)`, out.ID, entryID, ts); err != nil {
				return errs.Wrap(errs.CodeInternal, err, "cannot link attachment to entry")
			}
			out.EntryIDs = []int64{entryID}
		}
		_, err := s.q(ctx).ExecContext(ctx,
			`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
			repairID, actor, "attachment_add", detailJSON(map[string]any{
				"attachment_id": out.ID, "display_name": displayName, "size_bytes": stored.SizeBytes, "sha256": stored.SHA256,
			}), ts)
		return err
	})
	if err != nil {
		// Never leave orphan bytes behind a failed metadata write.
		_ = store.Delete(repairID, stored.StorageName)
		return Attachment{}, err
	}
	if out.EntryIDs == nil {
		out.EntryIDs = []int64{}
	}
	setPreview(&out)
	return out, nil
}

// GetAttachment reads one attachment with its entry links.
func (s *Service) GetAttachment(ctx context.Context, id int64) (Attachment, error) {
	a, err := s.scanAttachment(ctx, `SELECT `+attachmentCols+` FROM attachments WHERE id = ?`, id)
	if err != nil {
		return Attachment{}, err
	}
	links, err := s.attachmentEntryIDs(ctx, id)
	if err != nil {
		return Attachment{}, err
	}
	a.EntryIDs = links
	setPreview(&a)
	return a, nil
}

// ListRepairAttachments lists a repair's attachments (the gallery source).
// Normal views show only attached files: detached (recoverable archive) and
// purged rows appear only with includeAll; detach removes from normal
// views immediately).
func (s *Service) ListRepairAttachments(ctx context.Context, repairID int64, includeAll bool) ([]Attachment, error) {
	if _, err := s.GetRepairRow(ctx, repairID); err != nil {
		return nil, err
	}
	extra := ""
	if !includeAll {
		extra = " AND state = 'attached'"
	}
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT `+attachmentCols+` FROM attachments WHERE repair_id = ?`+extra+` ORDER BY id ASC`, repairID)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot list attachments")
	}
	defer func() { _ = rows.Close() }()
	out := []Attachment{}
	for rows.Next() {
		a, err := scanAttachmentRow(rows)
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot read attachment row")
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		links, err := s.attachmentEntryIDs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].EntryIDs = links
		setPreview(&out[i])
	}
	return out, nil
}

// SetCaption updates the editable per-photo caption; audited.
func (s *Service) SetCaption(ctx context.Context, actor string, id int64, caption string) (Attachment, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Attachment{}, err
	}
	current, err := s.GetAttachment(ctx, id)
	if err != nil {
		return Attachment{}, err
	}
	if current.State == "purged" {
		return Attachment{}, errs.New(errs.CodeConflict, "attachment %d is purged", id)
	}
	caption, err = validateText("caption", caption, 1000, false)
	if err != nil {
		return Attachment{}, err
	}
	ts := s.timestamp()
	err = s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE attachments SET caption=?, updated_at=? WHERE id=?`, caption, ts, id); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot update caption")
		}
		_, err := s.q(ctx).ExecContext(ctx,
			`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
			current.RepairID, actor, "attachment_caption", detailJSON(map[string]any{
				"attachment_id": id, "from": current.Caption, "to": caption,
			}), ts)
		return err
	})
	if err != nil {
		return Attachment{}, err
	}
	return s.GetAttachment(ctx, id)
}

// LinkEntry / UnlinkEntry manage the attachment<->entry association.
// Cross-repair links are refused; both operations are audited.
func (s *Service) LinkEntry(ctx context.Context, actor string, id, entryID int64) (Attachment, error) {
	return s.setEntryLink(ctx, actor, id, entryID, true)
}

// UnlinkEntry removes one entry association (bytes stay on the repair).
func (s *Service) UnlinkEntry(ctx context.Context, actor string, id, entryID int64) (Attachment, error) {
	return s.setEntryLink(ctx, actor, id, entryID, false)
}

func (s *Service) setEntryLink(ctx context.Context, actor string, id, entryID int64, link bool) (Attachment, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Attachment{}, err
	}
	a, err := s.GetAttachment(ctx, id)
	if err != nil {
		return Attachment{}, err
	}
	e, err := s.getEntry(ctx, entryID)
	if err != nil {
		return Attachment{}, err
	}
	if e.RepairID != a.RepairID {
		return Attachment{}, errs.New(errs.CodeValidation,
			"entry %d belongs to repair %d; attachments may only link inside their own repair (%d)", entryID, e.RepairID, a.RepairID)
	}
	ts := s.timestamp()
	action := "attachment_unlink"
	if link {
		action = "attachment_link"
	}
	err = s.InTx(ctx, func(ctx context.Context) error {
		if link {
			if _, err := s.q(ctx).ExecContext(ctx,
				`INSERT OR IGNORE INTO attachment_links(attachment_id, entry_id, created_at) VALUES(?,?,?)`, id, entryID, ts); err != nil {
				return errs.Wrap(errs.CodeInternal, err, "cannot link attachment")
			}
		} else {
			res, err := s.q(ctx).ExecContext(ctx,
				`DELETE FROM attachment_links WHERE attachment_id = ? AND entry_id = ?`, id, entryID)
			if err != nil {
				return errs.Wrap(errs.CodeInternal, err, "cannot unlink attachment")
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return errs.New(errs.CodeConflict, "attachment %d is not linked to entry %d", id, entryID)
			}
		}
		_, err := s.q(ctx).ExecContext(ctx,
			`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
			a.RepairID, actor, action, detailJSON(map[string]any{"attachment_id": id, "entry_id": entryID}), ts)
		return err
	})
	if err != nil {
		return Attachment{}, err
	}
	return s.GetAttachment(ctx, id)
}
