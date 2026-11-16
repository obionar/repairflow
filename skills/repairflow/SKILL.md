---
name: repairflow
description: Orient, build and change RepairFlow Go safely.
license: AGPL-3.0-only
---

# RepairFlow development

Use this when starting any RepairFlow coding task. The public repository is the source of truth;
the application is one Go binary with an embedded web UI and SQLite.

## Orient

Resolve the actual `SKILL.md` target (for example with `readlink -f` on the loaded skill path)
before following repository-relative paths. For a symlinked skill, the checkout is three directories
above that resolved file. If the skill was copied or that checkout is unavailable, locate or ask for
the intended RepairFlow checkout; never infer it from the current directory. Set `REPO` to its
absolute root, verify `go.mod` declares `module repairflow`, and inspect `git -C "$REPO" status -sb`
and `git -C "$REPO" log -1`. Run the commands below from `cd "$REPO"`.

Read, in order (paths are relative to that verified checkout):

1. `AGENTS.md` — safety rules and definition of done.
2. `docs/ARCHITECTURE.md` — approved invariants, schema and trade-offs.
3. `docs/DEVELOPMENT.md` — commands, change ownership and debugging.
4. `docs/CLI-CONTRACT.md` — interface you must not break silently.

## Build and test

```bash
make verify                  # gofmt check, vet and the Go test suite
make build                   # ignored linux/amd64 binary in the repository root
go test -count=1 ./...       # bypass the test cache while iterating
go test -race -count=1 ./...
scripts/verify-all.sh        # full local gate, fixed order and timing summary
scripts/verify-all.sh --with-fuzz
make cross                   # static linux/amd64 + linux/arm64
```

Browser tests use system Chromium:

```bash
cd tests/e2e && go test -count=1 -v .
```

`make verify` is the minimum. Run the full gate for a broad refactor, contract change or release.
ARM64 compilation is not ARM64 runtime proof; the tested target is Linux arm64.

## Disposable implementation loop

```bash
make build
REPAIRFLOW_BIN="$REPO/repairflow" ./scripts/run-demo.sh  # foreground; Ctrl+C removes workspace
```

For manual CLI work create a disposable workspace explicitly:

```bash
WS=$(mktemp -d "${TMPDIR:-/tmp}/rf-dev.XXXXXX")
./repairflow version          # verify the build identity, not an installed PATH binary
./repairflow init --home "$WS"
./repairflow --json customer list --home "$WS"
```

Never point a command, test, smoke script or example at a real workspace, `~/.repairflow`, an
installed service or customer data. Seeding refuses to mix demo and real records; do not bypass
that refusal.

## Change ownership

- `internal/app` — domain rules, transactions, revisions, audit, auth and money.
- `internal/db` — SQLite open/pragmas, embedded migrations, integrity.
- `internal/files` — attachment bytes, staging, hashes and recoverable deletion.
- `internal/cli` — parsing, human output, JSON envelopes, exit codes.
- `internal/web` — routes, forms, templates, auth/CSRF middleware, static assets.
- `internal/backup`, `internal/update` — verified archives and signed offline replacement.
- `internal/workspace`, `internal/service`, `internal/seed` — lifecycle, init systems and demo data.

Business logic never belongs in handlers or CLI parsing. Domain SQL and transactions belong in
`internal/app`; `internal/db` owns the SQLite connection, schema and integrity checks. Add
migrations; never rewrite an applied one.

## Contract ownership

Use `docs/CLI-CONTRACT.md` and `skills/repairflow-cli/SKILL.md` for flags, JSON, revisions,
money input and record operations; `docs/ARCHITECTURE.md` for domain invariants; and
`docs/SECURITY.md` for authentication, exposure and retention limits. Preserve these contracts
and add regression coverage rather than copying their tables here.

Build defaults come from `Makefile`; do not infer a release version from this skill. Packaging,
installation and service/update mechanics belong to `skills/repairflow-deploy/SKILL.md`.

## Definition of done

The behavior is implemented, `make verify` passes, the regression test covers the new path, public
docs match the code, and no real data, secret, binary or absolute local path entered the repository.
Run the full gate for broad or release changes and report the exact command output.
