# Project map

Use this page to locate implementation and test ownership. Coding agents should also read
[AGENTS.md](../AGENTS.md) and load the matching guide under [`skills/`](../skills/).

Run `make symbols` to generate the ignored `dist/SYMBOLS.md` declaration index. Do not hand-edit or
commit generated output.

## Request flow

```text
cmd/repairflow → internal/cli → command dispatch
                              ├→ internal/app.Service → SQLite + files.Store
                              ├→ workspace/config/db/backup/update/service/seed
                              └→ internal/web → middleware → handlers → app.Service
```

`internal/app` owns business rules, transactions, revisions and audit writes. CLI and web adapters
parse input and render output. `internal/files` owns attachment paths and original bytes.

## Source tree

| Path | Responsibility |
|---|---|
| `cmd/repairflow/main.go` | Process startup, signals and exit |
| `cmd/symbols/main.go` | AST-based symbol-map generator |
| `internal/cli/cli.go` | Command dispatch, global flags and workspace selection |
| `internal/cli/args.go` | Strict subcommand parsing |
| `internal/cli/domain.go` | Shared service opening and actor/ID/page parsing |
| `internal/cli/init.go`, `init_wizard.go` | Workspace initialization |
| `internal/cli/output.go`, `render.go` | Text and JSON output |
| `internal/cli/customers.go`, `repairs.go` | Customer and repair commands |
| `internal/cli/attachments.go`, `parts.go` | Attachment, part and estimate commands |
| `internal/cli/users.go`, `serve.go` | Account and web-server commands |
| `internal/cli/backup.go`, `update.go`, `service.go` | Lifecycle operations |
| `internal/cli/diagnostics.go`, `debug.go` | Health checks and debug reporting |
| `internal/config/` | Strict TOML, validation and workspace path safety |
| `internal/workspace/` | Directory layout, initialization and cleanup |
| `internal/db/db.go` | SQLite open, pragmas, migration runner and integrity |
| `internal/db/migrations.go` | Embedded schema migrations |
| `internal/errs/errs.go` | Stable error codes and exit mapping |
| `internal/logging/` | Shared opt-in structured logging |
| `internal/app/app.go`, `types.go` | Service, models, validation and transactions |
| `internal/app/customers.go` | Customer CRUD, search and history |
| `internal/app/repairs.go` | Repair CRUD, stages, entries and listing |
| `internal/app/workflow.go` | Pickup, entry recovery and archive workflows |
| `internal/app/money.go`, `parts.go`, `estimates.go` | Integer money, parts and estimates |
| `internal/app/dashboard.go` | Open-work and awaiting-pickup queues |
| `internal/app/attachments.go` | Attachment metadata, upload and entry links |
| `internal/app/attachment_lifecycle.go` | Retention and destructive operations |
| `internal/app/attachment_reconcile.go` | Metadata and original-byte reconciliation |
| `internal/app/auth.go` | Users, Argon2id, sessions and roles |
| `internal/app/idempotency.go` | Intake replay and conflict handling |
| `internal/app/fingerprint.go` | Demo content provenance |
| `internal/files/` | Streaming, hashing, publishing, export and recoverable deletion |
| `internal/backup/` | Verified `.rfbak` creation, validation and restore |
| `internal/update/` | Signed manifests and verified binary replacement |
| `internal/service/` | Named systemd user units and OpenRC scripts |
| `internal/seed/` | Deterministic synthetic data, media and reset |
| `internal/web/server.go` | Server construction, routes, templates and listener |
| `internal/web/middleware.go` | Authentication, host, CSRF and request logging |
| `internal/web/render.go` | Page rendering, errors and sessions |
| `internal/web/throttle.go` | Login throttling |
| `internal/web/handlers.go` | Shared form and error helpers |
| `internal/web/accounts.go`, `customers.go`, `repairs.go` | Account, customer and repair pages |
| `internal/web/parts.go`, `attachments.go` | Part, estimate and attachment pages |
| `internal/web/templates/` | Server-rendered pages and stable selectors |
| `internal/web/static/` | Embedded CSS and progressive-enhancement JavaScript |
| `internal/i18n/` | Translation loading, fallback, plurals and catalogs |
| `tests/e2e/` | Real-binary Chromium workflows; separate Go module |
| `tests/*.py` | Disposable CLI lifecycle and hostile-input smoke tests |

## Contracts and tests

| Area | Authority | Reference and tests |
|---|---|---|
| CLI flags, JSON, errors and exits | `internal/cli`, `internal/errs` | [CLI contract](CLI-CONTRACT.md), `internal/cli/*_test.go` |
| Configuration and home precedence | `internal/config` | `internal/config/*_test.go`, fuzz targets |
| Schema and migrations | `internal/db/migrations.go` | [Architecture](ARCHITECTURE.md), database/app/backup tests |
| Domain workflows | `internal/app` | [Architecture](ARCHITECTURE.md), `internal/app/*_test.go` |
| Attachment bytes and retention | `internal/files`, `internal/app` | files, app, web and backup tests |
| HTTP routes, auth, CSRF and roles | `internal/web` | HTTP tests and `tests/e2e/` |
| Templates and stable selectors | `internal/web/templates`, `static` | Browser workflows in `tests/e2e/` |
| Backup and update formats | `internal/backup`, `internal/update` | Package roundtrip and fault tests |
| Workspace, services and demo | `internal/workspace`, `service`, `seed` | [Daily use](USAGE.md), [Deployment](DEPLOY.md), package tests |
| Translations | `internal/i18n` | [Translations](TRANSLATIONS.md), catalog parity tests |
