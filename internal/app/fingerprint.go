package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"repairflow/internal/errs"
)

// BusinessFingerprint hashes the full business content of the workspace
// (content fields of every business table, in stable id order). Timestamps
// are deliberately excluded. Used by the demo-reset provenance guard
// in a verified, isolated demo workspace and useful for diagnostics.
func (s *Service) BusinessFingerprint(ctx context.Context) (string, error) {
	h := sha256.New()
	dump := func(query string) error {
		rows, err := s.q(ctx).QueryContext(ctx, query)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot fingerprint business data")
		}
		defer func() { _ = rows.Close() }()
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			var line strings.Builder
			for _, v := range vals {
				switch x := v.(type) {
				case []byte:
					line.Write(x)
				case string:
					line.WriteString(x)
				default:
					line.WriteString(formatAny(x))
				}
				line.WriteByte(0x1f)
			}
			h.Write([]byte(line.String()))
			h.Write([]byte{0x0a})
		}
		return rows.Err()
	}

	queries := []string{
		`SELECT id, name, phone, email, address, notes, archived_at FROM customers ORDER BY id`,
		`SELECT id, customer_id, device, device_type, brand, model, serial, issue, stage, notes, pickup_at, picked_up_by, pickup_notes, archived_at FROM repairs ORDER BY id`,
		`SELECT id, repair_id, kind, body, deleted_at FROM repair_entries ORDER BY id`,
		`SELECT id, repair_id, description, quantity_milli, unit, unit_cost_cents, unit_price_cents, notes, deleted_at FROM repair_parts ORDER BY id`,
		`SELECT id, repair_id, version, status, subtotal_cents, discount_kind, discount_value, discount_cents, total_cents FROM estimates ORDER BY id`,
		`SELECT id, estimate_id, description, quantity_milli, unit, unit_price_cents, line_total_cents FROM estimate_lines ORDER BY id`,
		`SELECT id, repair_id, display_name, sha256, size_bytes, caption, source, state FROM attachments ORDER BY id`,
		`SELECT attachment_id, entry_id FROM attachment_links ORDER BY attachment_id, entry_id`,
	}
	for _, q := range queries {
		if err := dump(q); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func formatAny(v any) string {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case nil:
		return ""
	default:
		return ""
	}
}
