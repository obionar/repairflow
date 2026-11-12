# Development

RepairFlow is a dependency-light Go application: one static binary, server-rendered HTML, embedded
assets and SQLite. There is no frontend build step, background worker or outbound network dependency.

Read [AGENTS.md](../AGENTS.md) before changing the repository. It defines safety rules, change
ownership and the definition of done. [Architecture](ARCHITECTURE.md) explains domain invariants;
[CLI contract](CLI-CONTRACT.md) defines the machine interface.

## Prerequisites

- Go 1.27 and `make`.
- `go-licenses` v1.6.0 for dependency and notice verification.
- Chromium for browser end-to-end tests.
- Python 3 for the optional CLI smoke scripts.
- The tools invoked by `scripts/verify-all.sh` for the complete local gate.

There is no CI or GitHub Actions gate. Local verification is required before submitting a change.

## Build and run

```bash
make build
./scripts/run-demo.sh
```

The demo launcher builds a temporary synthetic workspace, starts the web interface and removes the
workspace on exit. For manual CLI work, always choose a disposable workspace explicitly:

```bash
./repairflow init --home /tmp/repairflow-dev
./repairflow --json customer list --home /tmp/repairflow-dev
```

Never use a real workspace, customer records or an installed service for development or testing.

## Verification

Use the fast gate while iterating:

```bash
make verify
go test -race -count=1 ./...
```

Run browser workflows after web changes:

```bash
cd tests/e2e && go test -count=1 -v .
```

Before a broad change or release candidate, run the full gate:

```bash
./scripts/verify-all.sh
./scripts/verify-all.sh --with-fuzz   # optional bounded fuzzing
```

Useful disposable smoke tests after `make build`:

```bash
python3 tests/synthetic_lifecycle.py ./repairflow /tmp/repairflow-lifecycle
python3 tests/hostile_cli.py ./repairflow /tmp/repairflow-hostile
```

Use fresh directories for those scripts. Do not point them at an existing workspace.

Visual changes also require real browser review at mobile and desktop widths, keyboard and
no-JavaScript workflows, and an accessibility check on the affected pages. A template assertion or
HTTP 200 response alone does not verify the UI.

## Change ownership

- `internal/app` owns business rules, transactions, revisions, authentication and audit writes.
- `internal/db` owns SQLite setup, schema migrations and integrity checks.
- `internal/files` owns attachment paths, original bytes and recoverable deletion.
- `internal/cli` and `internal/web` are adapters: parse input, call the app layer and render output.
- `internal/backup` and `internal/update` own archive and binary-replacement formats.
- `internal/workspace`, `internal/service` and `internal/seed` own lifecycle, service units and demo
  data.

Keep SQL and business rules out of handlers and CLI parsing. Add regression coverage for behavior
changes, including failure paths. Use synthetic fixtures and test-only fault injection; never weaken
a safety check to make a test pass.

## Public contracts

Extend CLI flags, JSON fields, error codes, exit codes, routes and stable web selectors additively.
Do not silently repurpose them. Text output is for humans; `--json` is the supported automation
interface.

Schema changes require a new numbered, transactional migration. Never edit an applied migration.
Preserve original attachment bytes and verify backup/restore implications. Update the relevant docs
and [project map](PROJECT_MAP.md) with structural or user-visible changes.

Run `make symbols` after declaration changes to generate the ignored `dist/SYMBOLS.md`. Do not commit
generated symbols, binaries, release directories, databases, attachments, backups, secrets or local
machine details.

## License inventory and notices

RepairFlow is AGPL-3.0-only. Install the pinned license tool once:

```bash
go install github.com/google/go-licenses@v1.6.0
```

Then verify the compiled-dependency inventory, bundled notices and first-party assets:

```bash
make license-check
```

After changing dependencies or reviewed assets, regenerate `THIRD_PARTY_NOTICES.md` with
`make licenses` and commit the result. New static/media assets must be reviewed and recorded in
`licenses/first-party-assets.txt`. Unknown licenses, missing notices and unreviewed assets fail the
check. Release bundles include `LICENSE`, `THIRD_PARTY_NOTICES.md` and a corresponding-source
archive; every bundle member is covered by `SHA256SUMS`. The release script refuses a missing or
stale inventory. The browser-test-only module is reported separately under `dist/licenses/` and is
not bundled with the application.

## Debugging

Use `--debug` or `REPAIRFLOW_DEBUG=1` for structured diagnostics. HTTP responses include
`X-Request-ID` for correlation; internal CLI errors include a stack trace. Debug logs exclude
passwords, cookies, session and CSRF tokens, authorization headers, query strings, customer payloads
and file contents.

For a focused test with Delve:

```bash
dlv test ./internal/web/ -- -test.run TestName -test.v
```

See [Project map](PROJECT_MAP.md) for package and test locations.
