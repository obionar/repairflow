# Architecture

RepairFlow is a single Go executable with two adapters over one application service:

```text
CLI arguments → internal/cli ─┐
                              ├→ internal/app.Service → SQLite
Browser       → internal/web ─┘                    └→ original attachment files
```

Workspace, configuration, backup, update, service and demo operations live in their own packages.
The web interface uses `net/http`, `html/template` and embedded assets. JavaScript is optional
progressive enhancement, not a requirement for core workflows.

## Components

- `cmd/repairflow` starts the process and handles signals and exit status.
- `internal/app` owns validation, business rules, transactions, revisions and audit records.
- `internal/db` owns SQLite setup, embedded migrations and integrity checks.
- `internal/files` owns opaque attachment paths, streaming, hashing and original bytes.
- `internal/cli` and `internal/web` parse requests and render results; they do not duplicate domain
  rules.
- `internal/backup`, `internal/update` and `internal/service` implement archive, binary replacement
  and service-lifecycle operations.

SQLite uses STRICT tables, foreign keys, WAL, `synchronous=FULL`, a busy timeout and one connection
per process. `Service.InTx` composes application writes in a transaction. Content-derived revisions
let callers request optimistic-concurrency checks.

The main runtime dependencies are `modernc.org/sqlite`, `golang.org/x/crypto` for Argon2id and
`golang.org/x/term`. Browser-test tooling is a separate Go module.

## Domain invariants

### Repairs and history

- A repair records device type, brand, model, serial number, issue and notes. `device` is a display
  alias derived from those fields.
- The customer is optional at intake. An unassigned repair can be assigned or cleared later; the
  JSON representation uses `customer_id: 0` and `unassigned: true`.
- Stages are `new`, `in_progress`, `waiting_parts`, `lab_test`, `customer_test`, `repaired` and
  `no_repair`. Returning terminal work to `in_progress` requires an explicit audited reopen.
- Entry kinds are `work`, `test`, `contact`, `handover` and `note`. Corrections preserve immutable
  prior revisions; entry deletion is soft and reversible.
- Customers and repairs archive rather than hard-delete. Pickup is audited and independent of stage.
- The dashboard contains read-only open-work and awaiting-pickup queues, not performance metrics.

### Parts, estimates and money

- Part lines are free text with positive integer milli-quantities and an explicit unit, defaulting
  to `pcs`.
- Cost and price are separate gross EUR values stored as integer cents. Cost may be unknown; price
  is non-negative.
- Estimates are immutable snapshots of active part lines. Accepting one supersedes the previously
  accepted estimate.
- Rounding is deterministic integer half-up. Amount discounts above the subtotal are refused.
- Estimates are not payments. No payment, settlement, inventory or tax-invoice ledger exists.

### Attachments and retention

- Uploads are staged, size-checked and SHA-256 hashed before being published under opaque storage
  names. Original bytes are never rewritten.
- The default per-file limit is 256 MiB. Only sniffed JPEG, PNG and WebP files up to 8 MiB preview
  inline; other files download unchanged.
- An attachment can link to multiple entries on the same repair, never across repairs.
- Detach is recoverable. Permanent purge requires admin confirmation and at least 30 days detached;
  metadata and audit history survive. There is no automatic purge.

### Intake, accounts and demo data

- Customer and repair creation accept an optional idempotency key. An identical retry replays the
  original result; reusing the key with a different payload conflicts.
- Accounts use Argon2id password hashes and admin, technician or viewer roles. Sessions are hashed,
  expiring and revocable; password and active-state changes revoke affected sessions.
- Demo records are synthetic and cannot be seeded over business records. Reset requires a valid
  demo marker and unchanged content fingerprint; users and sessions survive.

## Schema

The authoritative DDL is [`internal/db/migrations.go`](../internal/db/migrations.go). Migrations
are embedded, numbered, transactional and forward-only. Never edit an applied migration. A database
or configuration created by a newer schema version is refused rather than downgraded.

| Version | Purpose |
|---|---|
| v1 `initial_domain` | Customers, repairs, entries, activity, history and seed markers |
| v2 `workflow_completion` | Pickup, archive, entry soft deletion and entry revisions |
| v3 `parts_estimates` | Parts, immutable estimates and integer money/quantity fields |
| v4 `attachments` | Attachment metadata, opaque names, hashes, retention and entry links |
| v5 `request_idempotency` | Durable intake keys, request fingerprints and stored results |
| v6 `users_sessions` | Users, roles, password hashes, sessions, CSRF and revocation |
| v7 `contact_device_parity` | Customer address and structured device fields |
| v8 `drop_legacy_ticket_id` | Removal of the obsolete legacy identifier |
| v9 `repair_customer_optional` | Nullable repair customer for unassigned intake |

`schema_migrations` tracks applied versions. `doctor` runs database integrity and foreign-key checks
and reconciles attachment metadata with stored bytes. Revisions are derived from current content,
not stored as independent version numbers.

## Workspace and failure handling

A workspace contains `config.toml`, `data/repairflow.db`, `files/`, `backups/`, `logs/`, `cache/`
and `run/`. Configuration is strict and is not silently rewritten. User-supplied filenames are
metadata, never storage paths. The workspace-owning OS account is the outer trust boundary.

The filesystem and SQLite cannot commit atomically together. Upload staging and `doctor`
reconciliation expose missing or orphaned bytes. Backups combine a consistent `VACUUM INTO` database
snapshot with hash-verified original files; restore verifies the complete archive before publishing
it into an empty destination.

Attachment purge and demo reset use a cross-process workspace lock and same-filesystem recovery
links under `files/.purge/`. Original bytes are preserved until the database transaction commits,
restored on rollback and recovered after interruption. Cleanup never removes those recovery links.

Updates authenticate and verify the artifact before installation. The updater requires regular
files, stages and re-hashes `<executable>.new`, preserves `<executable>.prev`, verifies the published
target and health-checks the verified bytes. Replacement or health-check failure rolls back. Verified
apply is Linux-only; unsigned builds and downgrades are refused. The optional
`scripts/update-github.sh` downloads and safely extracts the exact architecture archive for a GitHub
release tag, then hands the verified local binary and signed manifest to this same path; the
application performs no background update checks.

Errors use stable codes from `internal/errs`. Expected validation, permission, conflict and
not-found failures remain distinct from internal errors. Optional structured diagnostics and HTTP
request IDs aid tracing without exposing credentials, payloads or file contents.

Payment accounting, stock operations, Markdown import, MCP, embedded AI and messaging runtimes are
intentionally outside the current architecture.
