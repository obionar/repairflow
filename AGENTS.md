# RepairFlow agent guide

This is the public source repository for RepairFlow: one Go executable, a server-rendered web UI,
SQLite, local files and no runtime service. Read this file before changing code. It is the entry
point for any coding agent; the detailed contracts are under `docs/`.

## First commands

```bash
make verify                 # gofmt check, vet and the Go test suite
make build                  # static linux/amd64 binary named repairflow
./scripts/run-demo.sh       # disposable synthetic workspace on 127.0.0.1:8091
./scripts/verify-all.sh     # full local gate; --with-fuzz adds bounded fuzzing
```

`make build` produces an ignored binary in the repository root. Never commit build output.
Linux amd64 and arm64 are the tested targets. macOS, Windows and other Unixes are not tested yet;
contributions are welcome, but do not describe them as supported.

## Non-negotiable safety

- Use a disposable workspace (`--home /absolute/path` or a temporary directory) for every test.
  Never point a command, test or example at a real workspace, production records, another user's
  data, `~/.repairflow` or an installed service.
- Never commit binaries, databases, attachments, backups, signing keys, `.env` files, customer data,
  local hostnames or absolute home paths.
- The application has no outbound network calls, telemetry, AI runtime or background jobs. Do not
  add one as a shortcut.
- Do not disable signature/hash/size checks, path-safety checks, CSRF, password hashing or auth to
  make a test pass. Do not add production fault injection; failure injection belongs in tests.
- Back up before a destructive or upgrade operation. A restore targets an empty/new workspace only.
- Do not publish a release, push a tag, deploy to a host or change a real service without explicit
  owner authorization. There is no CI or GitHub Actions gate; local verification is authoritative.

## Repository map

| Area | Authority |
|---|---|
| Domain rules, transactions, revisions, audit | `internal/app` |
| SQLite schema, migrations, integrity | `internal/db` |
| Original attachment bytes and retention | `internal/files` |
| CLI parsing, JSON envelopes, exit codes | `internal/cli`, `internal/errs` |
| HTTP routes, forms, templates, auth, CSRF | `internal/web` |
| Backup/restore and signed updates | `internal/backup`, `internal/update` |
| Workspace layout and disposable cleanup | `internal/workspace` |
| systemd/OpenRC service units | `internal/service` |
| Synthetic demo data | `internal/seed` |
| Public contracts | `docs/CLI-CONTRACT.md`, `docs/ARCHITECTURE.md` (Schema) |
| Domain invariants | `docs/ARCHITECTURE.md` |
| Build, tests and debugging | `docs/DEVELOPMENT.md` |
| Install, deploy and operate | `docs/INSTALL.md`, `docs/DEPLOY.md`, `docs/USAGE.md` |

## Development rules

- Keep the Go server-rendered and dependency-light: no Node, Python, bundler, framework or database
  server. Small embedded JavaScript is allowed only as progressive enhancement.
- Extend public contracts; do not silently rename CLI flags, JSON fields, error codes, exit codes,
  routes or stable web selectors. Document a deliberate break in `docs/ARCHITECTURE.md` and the
  commit message.
- Add schema changes only as new numbered, transactional migrations. Keep backup formats, original
  bytes and existing records stable.
- Use integer cents for money and integer milli-quantities for parts. Estimates are not payments.
- Follow the existing ownership boundaries: business rules in `internal/app`, SQL only in `internal/db`,
  files only in `internal/files`, presentation in `internal/cli` and `internal/web`.
- Add a regression test before fixing behavior. Prove failure paths with disposable workspaces and
  fault injection, not by weakening a check.
- Keep the public tree free of internal plans, verification logs, unreviewed screenshots, UI-parity
  matrices and generated symbol maps. `make symbols` writes `dist/SYMBOLS.md`; do not hand-edit it.
- Update `README.md` and the relevant `docs/` page in the same change as user-visible behavior.
- Do not add placeholder features for deferred work. If a capability is not implemented, say so.

## Before changing code

1. Read `AGENTS.md`, `docs/ARCHITECTURE.md` and the package tests around the area.
2. Check the public contract in `docs/CLI-CONTRACT.md` or the Schema section of
   `docs/ARCHITECTURE.md`.
3. Create a disposable workspace; never use a real one.
4. Make the smallest behavior-preserving change that solves the task.
5. Run `make verify`; for a release or broad change run `./scripts/verify-all.sh`.
6. Update the map and docs if declarations or user-visible behavior changed.

## Deploy and operate

Do not invent deployment steps. Follow:

- `docs/INSTALL.md` — binary installation, demo, first account, workspace layout and troubleshooting.
- `docs/DEPLOY.md` — remote access, systemd/OpenRC services, workspace moves, upgrades and removal.
- `docs/USAGE.md` — daily web/CLI workflows, attachments, languages, backup/restore and diagnostics.

The binary must live at a stable path before `service install`; the unit pins its absolute path.
A real deployment needs a verified backup and explicit owner authorization. Unsigned builds cannot
use `update apply`; that refusal is deliberate. Release bundles must include `LICENSE`; run
`make license-check` before packaging.

## Skills

Load the task-specific skill instead of reconstructing commands:

- [`skills/repairflow/SKILL.md`](skills/repairflow/SKILL.md) — repository orientation, build/test and
  safe change workflow.
- [`skills/repairflow-cli/SKILL.md`](skills/repairflow-cli/SKILL.md) — CLI operations, JSON contract
  and disposable-workspace rules.
- [`skills/repairflow-deploy/SKILL.md`](skills/repairflow-deploy/SKILL.md) — release, install,
  service, backup and upgrade procedure.

## Definition of done

- The requested behavior is implemented, not stubbed.
- `make verify` passes; `./scripts/verify-all.sh` passes for broad/release changes.
- New behavior has a regression test; failure paths remain covered.
- No real data, secret, binary or absolute local path entered the repository.
- Public docs and contracts match the code.
- No push, tag, release, deployment or production change happened without explicit authorization.
