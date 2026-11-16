---
name: repairflow-deploy
description: Release, install, back up and upgrade RepairFlow.
license: AGPL-3.0-only
---

# RepairFlow deploy and operate

## Resolve scope and source

Resolve the actual `SKILL.md` target (for example with `readlink -f` on the loaded skill path).
The checkout is three directories above that resolved file; if copied or unavailable, locate or
ask for the intended checkout. Set `REPO` to its absolute root, verify `go.mod` declares
`module repairflow`, and read `AGENTS.md`. Resolve all `docs/`, `scripts/` and `skills/` references
against that root, not the caller's current directory. Read `docs/INSTALL.md`, `docs/DEPLOY.md`
and `docs/USAGE.md`; verify concrete steps against the installer/parser when docs disagree.

For an installed app, select a trusted absolute `BIN` path and read `"$BIN" version` and
`"$BIN" help`. Match docs/source to that version/commit: checkout instructions do not establish
what an older installed binary supports. Inspect the intended workspace and actual service unit,
including its executable, owner, name and home; do not infer them from defaults or prior runs.

Examples below are recipes, not permission. Get explicit authorization for the exact target and
operation before installation, account/data changes, service actions, updates, network downloads
or publication. Use disposable workspaces for checks; never treat a live workspace as a fixture.

## Release artifact

From `cd "$REPO"`, after release approval and the full development verification gate:

```bash
scripts/release.sh --version X.Y.Z --key /secure/release-signing.key --out /fresh/output
```

Choose only an owner-approved version; read build defaults from `Makefile`, not this skill.
The script refuses a dirty tree, a `-dev` version and an existing output directory. A publishable
archive requires `--key` and its public key (`--pub` overrides the sibling `release-signing.pub`).
It runs the license inventory check, builds `linux/amd64` and `linux/arm64`, signs a manifest naming
the binary `repairflow`, embeds the public key and verifies the manifest with a checker build.
It writes exactly these release assets:

```text
repairflow_Linux_x86_64.tar.gz
repairflow_Linux_arm64.tar.gz
SHA256SUMS
SHA256SUMS.sig
```

Each archive contains exactly `repairflow`, `LICENSE`, `manifest.json`, `manifest.sig`,
`update-github.sh`, `repairflow-update` and `repairflow-autoupdate`. `SHA256SUMS` covers both
archives and is signed (`SHA256SUMS.sig`), because the manifest covers only the binaries; verify it
before extracting so the helper scripts and `LICENSE` are authenticated too. Do not upload loose
binaries, docs, manifests or demo scripts as separate release assets.
GitHub-generated source archives at the matching tag provide corresponding source for that build;
preserve source availability for distributed or modified builds too. Never put signing keys,
build output or release directories in git. Keep private keys out of chat/logs and backed up offline;
operators installing a signed release do not need the publisher's private key.

## Verify and install

Copy/download the target archive, `SHA256SUMS` and `SHA256SUMS.sig` from the approved release into a
fresh staging directory, then authenticate and verify there (BusyBox has no `--ignore-missing`):

```bash
# With an installed binary: verify the signed list against its embedded key.
repairflow update verify --file SHA256SUMS --signature SHA256SUMS.sig
# First install has no binary yet: compare the published fingerprint and verify with openssl, or
# check only the archive you downloaded (a bare checksum pass is not authentication).
grep '  repairflow_Linux_arm64.tar.gz$' SHA256SUMS > SHA256SUMS.one
sha256sum -c SHA256SUMS.one
tar -xzf repairflow_Linux_arm64.tar.gz
BIN_DIR="$HOME/.local/bin"
mkdir -p "$BIN_DIR"
install -m 0755 repairflow update-github.sh repairflow-update repairflow-autoupdate "$BIN_DIR/"
BIN="$BIN_DIR/repairflow"
"$BIN" version
```

Compare version AND commit with the approved artifact. Keep `LICENSE`, `manifest.json` and
`manifest.sig` or the complete archive. For source installation, review `$REPO/install-repairflow`
before invoking it: its default is `$HOME/.local/bin/repairflow`, with `REPAIRFLOW_PREFIX` selecting
another prefix. It builds, then installs under the executable lock, but can also run `init` on an
existing `REPAIRFLOW_HOME`/default workspace; it is not a data-free verification command.

The default user-owned installation needs no privilege. A deliberately selected shared
`/usr/local/bin` layout is root-owned and needs `sudo`/`doas` to install/replace it; never make it
writable by the service user. Keep the binary at a stable absolute path before service installation.
For upgrades, follow the backup/stop/apply/restart sequence below, not this fresh-install copy recipe.

## First run and service

For a synthetic foreground check from the verified checkout, use
`REPAIRFLOW_BIN="$BIN" "$REPO/scripts/run-demo.sh"`. This launcher is source-checkout only,
not a release member; its known credentials must never be reused for real records.

For an explicitly authorized new workspace, set `WS` to its absolute path and initialize it.
Create the first admin through the environment interface without a password literal in history,
arguments or logs. This example requires an interactive Bash terminal with tracing disabled;
an agent should use an approved secret-injection mechanism, not collect a password in chat:

```bash
"$BIN" init --home "$WS"
(
  set +x
  read -r -s -p 'New admin password: ' REPAIRFLOW_NEW_PASSWORD || exit 1
  printf '\n'
  export REPAIRFLOW_NEW_PASSWORD
  "$BIN" user add alice --role admin --home "$WS"
)
"$BIN" user list --home "$WS"       # confirm the intended account
"$BIN" doctor --home "$WS"
```

The UI is loopback-only by default. Keep authentication enabled for remote access and prefer an
SSH tunnel or TLS-terminating proxy. Trusted-LAN HTTP exposes passwords and sessions in cleartext;
`web.allow_insecure_lan=true` additionally permits unauthenticated LAN testing and must never hold
real records. See `docs/SECURITY.md` and `docs/DEPLOY.md` for exposure limits.

Install a service only after checking its final binary path, workspace and chosen init system:

```bash
"$BIN" service install --init systemd --home "$WS"  # may enable AND start the user unit
"$BIN" service status --init systemd --home "$WS"
# OpenRC alternative, only on that target:
doas "$BIN" service install --init openrc --home "$WS"
doas rc-service repairflow status
```

Use `--no-enable` to write without starting; `--name NAME` selects a unit, not a new workspace,
port or binary. Configure those separately and use the same name on all service/supervisor actions.
Unprivileged OpenRC installation can stage a script instead of activating it; inspect the result.
Unmarked or differently marked units are refused. Removal refuses an active service and must not
delete a hand-written unit. Read back the generated unit and supervisor status, not just CLI success.

## Backup, restore and upgrade

```bash
"$BIN" backup create --output /ABS/pre-upgrade.rfbak --home "$WS"
"$BIN" backup verify /ABS/pre-upgrade.rfbak
"$BIN" restore /ABS/pre-upgrade.rfbak --home /ABS/new-empty-workspace
"$BIN" doctor --home /ABS/new-empty-workspace
```

Backup output must be outside the source workspace. Restore targets a new/empty workspace only.
Verification checks structure, hashes and the database snapshot; rehearse a restore, login and
representative record/attachment readbacks before relying on it. Keep an access-controlled copy on
separate storage: archives contain records and password hashes and are not encrypted. Local
`locales/` overrides are excluded, so preserve them separately if needed.

Before an upgrade, verify that backup, inspect migration compatibility and stop the selected writer
before the new binary opens its workspace. Do not assume prerelease migration compatibility or that
`<exe>.prev` alone can undo a schema change. Use the exact published tag, including any `v` prefix;
the following `TAG` is a placeholder to set from the approved release, not a latest-version claim:

```bash
"$BIN_DIR/update-github.sh" --tag "$TAG" --binary "$BIN"           # download + check only
"$BIN_DIR/update-github.sh" --tag "$TAG" --binary "$BIN" --apply   # separate authorization
```

The helper downloads and verifies as the operator, may escalate only local replacement for a safe
root-owned executable, and leaves restart separate. Pass only a trusted installed `BIN`. The
archive also ships `repairflow-update` and `repairflow-autoupdate`; consult their matching source
and `docs/DEPLOY.md` before using them. Do not configure unattended activation without approval.

For offline signed updates, check first with
`"$BIN" update check --manifest manifest.json --signature manifest.sig`, then use
`"$BIN" update apply --file /ABS/staged/repairflow --manifest manifest.json --signature manifest.sig --confirm`.
Only root-owned replacement needs `sudo`/`doas`. Never disable signature/hash/size/path/version
checks. Unsigned builds cannot use signed apply; any manual replacement needs its own approved
backup, stopped-writer and rollback procedure. Keep `<exe>.lock`; verified apply is Linux-only.

Restart the selected supervisor, re-pin its unit if the binary path changed, open a domain command
(e.g. `"$BIN" customer list --per-page 1 --home "$WS"`) to apply pending migrations, then read back
version/commit, `doctor`, record counts, the unit's executable/home and an authenticated web page.
Report any unverified step. A successful binary swap alone does not prove the running service updated.

## Limits

The application itself has no outbound calls or built-in scheduler; the optional external update
scripts perform network access when invoked or scheduled. One owner account/workspace is the trust
boundary, not web-role isolation. There is no built-in TLS termination or encryption at rest.
Push, tag, release publication and deployment are separately authorized actions.
RepairFlow is AGPL-3.0-only: preserve `LICENSE` and corresponding-source availability; do not
relicense the project.
