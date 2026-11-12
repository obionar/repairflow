package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"repairflow/internal/errs"
)

// Request-key idempotency for intake operations. Semantics —
// documented and tested, not invented ad hoc:
//
//  1. A client may pass an opaque idempotency key (8-128 chars of
//     [A-Za-z0-9_.-]) with a create request.
//  2. The key binds operation + canonicalized request fingerprint (explicit
//     field order, never raw JSON layout) + the created record.
//  3. Repeating the request with the same key and payload replays the
//     original result with zero new writes.
//  4. Reusing the key with a different payload is a structured conflict.
//  5. Failed validation never consumes a key (the key row is written in the
//     same transaction as the record, on success only).
//  6. Concurrent same-key requests serialize on the primary key: exactly one
//     record is created; the rest replay.
//  7. Keys persist for the life of the workspace and never contain payload
//     data (only a SHA-256 fingerprint is stored).
//
// The same rules apply through every interface (CLI today, web/MCP later)
// because they live in this service, not in an adapter.

var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{8,128}$`)

// IdempotentResult wraps a replayed-or-created entity result.
type IdempotentResult struct {
	Replayed bool `json:"replayed"`
}

// fingerprint builds the canonical request fingerprint from ordered fields.
func fingerprint(operation string, fields ...string) string {
	h := sha256.New()
	h.Write([]byte(operation))
	for _, f := range fields {
		h.Write([]byte{0x1f})
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validateIdempotencyKey(key string) error {
	if !idempotencyKeyRe.MatchString(key) {
		return errs.New(errs.CodeValidation,
			"idempotency key must be 8-128 characters of [A-Za-z0-9_.-] (opaque, client-generated)")
	}
	return nil
}

// replayResult reads a stored result for a matching key/fingerprint and
// decodes it into out. Mismatched fingerprints are conflicts.
func (s *Service) replayResult(ctx context.Context, key, operation, want string, scope int64, out any) (bool, error) {
	var gotFingerprint, resultJSON, gotOperation string
	var gotScope int64
	err := s.q(ctx).QueryRowContext(ctx,
		`SELECT operation, fingerprint, result_json, scope_id FROM request_idempotency WHERE key = ?`, key).
		Scan(&gotOperation, &gotFingerprint, &resultJSON, &gotScope)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errs.Wrap(errs.CodeInternal, err, "cannot read idempotency record")
	}
	if gotFingerprint != want || gotOperation != operation || gotScope != scope {
		return false, errs.New(errs.CodeConflict,
			"idempotency key %q was already used for a different request payload", key)
	}
	if err := json.Unmarshal([]byte(resultJSON), out); err != nil {
		return false, errs.Wrap(errs.CodeInternal, err, "cannot decode replayed result for key %q", key)
	}
	return true, nil
}

// storeResult persists the idempotency record inside the caller's transaction.
func (s *Service) storeResult(ctx context.Context, key, operation, fp string, scope int64, result any) error {
	body, err := json.Marshal(result)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot encode idempotent result")
	}
	if _, err := s.q(ctx).ExecContext(ctx,
		`INSERT INTO request_idempotency(key, operation, fingerprint, result_json, scope_id, created_at)
		 VALUES(?,?,?,?,?,?)`, key, operation, fp, string(body), scope, s.timestamp()); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot record idempotency key")
	}
	return nil
}

// CreateRepairWithKey creates a repair with optional request-key idempotency
// (empty key = plain create). See the semantics block above.
func (s *Service) CreateRepairWithKey(ctx context.Context, actor string, in RepairInput, key string) (Repair, error) {
	if key == "" {
		return s.CreateRepair(ctx, actor, in)
	}
	if err := validateIdempotencyKey(key); err != nil {
		return Repair{}, err
	}
	actorNorm, err := normalizeActor(actor)
	if err != nil {
		return Repair{}, err
	}
	fp := fingerprint("repair.create",
		strconv.FormatInt(in.CustomerID, 10), in.Device, in.DeviceType, in.Brand, in.Model,
		in.Serial, in.Issue, in.Notes, actorNorm)

	// Validation happens up front so invalid requests never consume a key.
	device := in.Device
	if strings.TrimSpace(in.DeviceType) != "" {
		device = in.DeviceType
	}
	if _, err := validateText("device", device, maxDevice, true); err != nil {
		return Repair{}, err
	}
	if _, err := validateText("issue", in.Issue, maxIssue, true); err != nil {
		return Repair{}, err
	}
	if _, err := validateText("notes", in.Notes, maxNotes, false); err != nil {
		return Repair{}, err
	}
	if in.CustomerID < 0 {
		return Repair{}, errs.New(errs.CodeValidation, "customer_id must be zero (unassigned) or a positive id")
	}

	var repair Repair
	err = s.InTx(ctx, func(ctx context.Context) error {
		var replayed Repair
		hit, err := s.replayResult(ctx, key, "repair.create", fp, in.CustomerID, &replayed)
		if err != nil {
			return err
		}
		if hit {
			repair = replayed
			return nil
		}
		created, err := s.CreateRepair(ctx, actorNorm, in)
		if err != nil {
			return err
		}
		if err := s.storeResult(ctx, key, "repair.create", fp, in.CustomerID, created); err != nil {
			return err
		}
		repair = created
		return nil
	})
	if err != nil {
		return Repair{}, err
	}
	return repair, nil
}

// CreateCustomerWithKey creates a customer with optional request-key
// idempotency (empty key = plain create).
func (s *Service) CreateCustomerWithKey(ctx context.Context, actor string, in CustomerInput, key string) (Customer, error) {
	if key == "" {
		return s.CreateCustomer(ctx, actor, in)
	}
	if err := validateIdempotencyKey(key); err != nil {
		return Customer{}, err
	}
	actorNorm, err := normalizeActor(actor)
	if err != nil {
		return Customer{}, err
	}
	fp := fingerprint("customer.create", in.Name, in.Phone, in.Email, in.Notes, actorNorm)

	var customer Customer
	err = s.InTx(ctx, func(ctx context.Context) error {
		var replayed Customer
		hit, err := s.replayResult(ctx, key, "customer.create", fp, 0, &replayed)
		if err != nil {
			return err
		}
		if hit {
			customer = replayed
			return nil
		}
		created, err := s.CreateCustomer(ctx, actorNorm, in)
		if err != nil {
			return err
		}
		if err := s.storeResult(ctx, key, "customer.create", fp, 0, created); err != nil {
			return err
		}
		customer = created
		return nil
	})
	if err != nil {
		return Customer{}, err
	}
	return customer, nil
}

// NormalizeIdempotencyKey trims a CLI/agent-provided key ("" = not used).
func NormalizeIdempotencyKey(key string) string { return strings.TrimSpace(key) }
