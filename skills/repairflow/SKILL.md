---
name: repairflow
description: Orient, build and change RepairFlow Go safely.
---

# RepairFlow development

Use this when starting any RepairFlow coding task. The public repository is the source of truth;
the application is one Go binary with an embedded web UI and SQLite.

## Orient

Read, in order:

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
./scripts/run-demo.sh        # foreground, temporary workspace, Ctrl+C removes it
```

For manual CLI work create a disposable workspace explicitly:

```bash
./repairflow init --home /tmp/rf-dev
./repairflow --json customer list --home /tmp/rf-dev
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

Business logic never belongs in handlers or CLI parsing. SQL never leaks outside `internal/db`
except through the app layer. Add migrations; never rewrite an applied one.

## Contracts that bite

- JSON success `{ok,command,contract,data}`; failure `{error:{code,message}}` on stderr with empty
  stdout. Exit `0` success, `1` internal, `2` usage/validation/permission, `3` workspace/state
  conflict, `4` not found.
- Edits use the content-derived `revision` from `show`; omitting it is last-write-wins.
- A repair may be unassigned (`customer_id: 0`, `unassigned: true`); create from the web with a
  blank customer, then assign later. CLI: omit `--customer`, then `repair update ID --customer ID`
  or `--no-customer`.
- Repair stages: `new`, `in_progress`, `waiting_parts`, `lab_test`, `customer_test`, `repaired`,
  `no_repair`; terminal stages require `--reopen`.
- Entry kinds: `work`, `test`, `contact`, `handover`, `note`.
- Money is integer cents; parts use integer milli-quantities; estimates are not payments.
- Attachments preserve original bytes and SHA-256; detach is recoverable; purge requires
  confirmation and the retention period. Never delete `files/.purge/` recovery links by hand.
- The web listener is loopback-only by default. `web.allow_insecure_lan=true` is an explicit unsafe
  testing exception and must never hold real records.
- `make build`/`make cross` default to `0.1.0-dev`; a release must be explicitly stamped.

## Definition of done

The behavior is implemented, `make verify` passes, the regression test covers the new path, public
docs match the code, and no real data, secret, binary or absolute local path entered the repository.
Run the full gate for broad or release changes and report the exact command output.
