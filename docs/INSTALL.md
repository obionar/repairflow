# Install and first run

RepairFlow runs as one static executable. A built binary needs no Go installation, Python, Node,
database server or other runtime. It includes the CLI, web interface and SQLite storage layer.

## Requirements

- **Prebuilt binary:** Linux on x86-64 (`amd64`) or ARM64 (`arm64`).
- **Build from source:** Go 1.27 and `make`.
- **Browser:** a current browser for the web interface.
- **Storage:** local storage owned by the operating-system account that runs RepairFlow.

Linux amd64 and arm64 are tested. Other platforms are not currently supported targets.

## Install the executable

### One command from a source checkout

```bash
git clone https://github.com/obionar/repairflow
cd repairflow
./install-repairflow
```

The source installer requires `flock` (util-linux or a BusyBox build with the applet enabled).
It builds the binary and installs it to `~/.local/bin/repairflow`, a directory that belongs to the
operator, so neither the install nor a later update needs privilege. Set
`REPAIRFLOW_PREFIX=/usr/local` for the shared, root-owned layout instead: a regular user can then
execute the binary but not replace it, and the installer uses `doas` or `sudo` for that one step.
To update, run
`git pull && ./install-repairflow` again: the binary is replaced atomically
under the same executable lock as signed updates, existing workspaces and configuration are kept,
and each workspace database migrates automatically on the next command or
start, from any supported previous version. Restart a running RepairFlow
service separately to pick up the new binary. For a schema-changing upgrade, take and verify a backup
first and stop the service before the new binary opens the workspace; an executable-only rollback
does not revert a migrated schema. If the service was installed from
an older path such as `~/.local/bin/repairflow`, rerun `repairflow service
install --home /absolute/workspace` so the unit uses the new binary path.

### From a release archive

Download the archive for this machine, the signed checksum list and its signature, then
authenticate the list before extracting anything. `TAG` is the exact release tag; the release
public-key fingerprint is published with the release.

```bash
TAG=v0.2.0-rc5
ASSET=repairflow_Linux_arm64.tar.gz          # repairflow_Linux_x86_64.tar.gz on amd64
BASE=https://github.com/obionar/repairflow/releases/download/$TAG
wget -q "$BASE/$ASSET" "$BASE/SHA256SUMS" "$BASE/SHA256SUMS.sig"

# 1. Authenticate SHA256SUMS with the published 64-hex-character release key.
PUB=0000000000000000000000000000000000000000000000000000000000000000   # replace with the fingerprint
printf '302a300506032b6570032100%s' "$PUB" | xxd -r -p > release-signing.der
xxd -r -p SHA256SUMS.sig > SHA256SUMS.sig.bin
openssl pkeyutl -verify -pubin -inkey release-signing.der -keyform DER \
  -rawin -in SHA256SUMS -sigfile SHA256SUMS.sig.bin

# 2. Check the archive you downloaded (BusyBox sha256sum has no --ignore-missing).
grep "  $ASSET$" SHA256SUMS > SHA256SUMS.one
sha256sum -c SHA256SUMS.one

tar -xzf "$ASSET"
install -Dm0755 repairflow ~/.local/bin/repairflow
install -Dm0755 update-github.sh ~/.local/bin/update-github.sh
install -Dm0755 repairflow-update ~/.local/bin/repairflow-update
install -Dm0755 repairflow-autoupdate ~/.local/bin/repairflow-autoupdate
repairflow version
```

Step 1 is what authenticates the download: a bare `sha256sum -c` only proves that a file matches a
list that arrived with it. The signed list covers the archives, and therefore `LICENSE` and the
three update scripts whose hashes are not in `manifest.json`. With a RepairFlow binary already
installed, `repairflow update verify --file SHA256SUMS --signature SHA256SUMS.sig` performs the same
check against the key embedded in that build. That first download still trusts the release host
over HTTPS until the fingerprint above is compared with the installed binary's key — check it once
per key, not per download.

`install -D` creates `~/.local/bin` when it is missing, and nothing here needs privilege: the binary
belongs to you, so a signed update replaces it without `sudo`. On a shared or multi-user host,
install to `/usr/local/bin/repairflow` with `sudo` or `doas` instead: that directory is
root-owned, so a regular user can execute RepairFlow but cannot replace the binary. Keep that
property when installing that way; do not make the directory writable by the service account. A
signed update of such an installation needs `sudo`/`doas` for the apply step; see
[Deployment](DEPLOY.md).

Only the matching line is checked, so the second architecture's archive is not needed.

Prereleases are never GitHub `latest`; fetch them by exact tag from the releases page (for example
`…/releases/tag/v0.2.0-rc1`). Between prereleases there is **no migration promise**: a workspace may
need to be recreated instead of migrated, and an older binary refuses a newer schema. Take a verified
backup before opening a workspace with a newer binary.

Use `repairflow_Linux_arm64.tar.gz` on ARM64. The archive also contains `LICENSE`, `manifest.json`,
`manifest.sig` and the update helpers; keep the archive or those files with the installation to
preserve the license and support offline verification. Keep the
executable at a stable path: service units and updates refer to that location.

### From source

```bash
make build
install -Dm0755 repairflow ~/.local/bin/repairflow
repairflow version
```

If `~/.local/bin` is not on `PATH`, call `~/.local/bin/repairflow` directly
or add this to your shell profile:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

The examples below use `repairflow` for readability.

## Try the disposable demo

From a source checkout:

```bash
./scripts/run-demo.sh
```

or, from the public repository root as a fresh clone:

```bash
git clone https://github.com/obionar/repairflow
cd repairflow/demo
./run-demo.sh
```

The `demo/` launcher uses an existing binary, builds from source when Go is
available, or downloads and checksum-verifies the matching Linux release into
`demo/bin/`. It then creates a temporary synthetic workspace. `./dev.sh` keeps
one workspace under `demo/workspace/` for development; `./reset.sh` restores it
while the demo content is pristine. No SQLite database is committed: the dataset
is generated from `internal/seed`.

Open <http://127.0.0.1:8091/login> and use `demo` / `repairflow`. The launcher opens that page in
the local browser and prints the machine's other addresses (the demo serves loopback only; other
devices need explicit remote access, see `docs/DEPLOY.md`). The launcher creates a temporary
workspace filled with synthetic customers, repairs, entries, parts and attachments.
Press **Ctrl+C** to stop the server and remove that workspace.

Do not use the demo credentials or dataset for real work.

## Create a real workspace

The default workspace is `~/.repairflow`. To use another location, pass an absolute `--home PATH`
to every command or set `REPAIRFLOW_HOME`.

```bash
repairflow init --language en
export REPAIRFLOW_NEW_PASSWORD='use-a-long-unique-password'
repairflow user add admin --role admin
unset REPAIRFLOW_NEW_PASSWORD
repairflow doctor
repairflow serve
```

Open <http://127.0.0.1:8091/login> and sign in with the account you created. There is no web
sign-up page.

`init` supports `en`, `el`, `ru`, `tr`, `es`, `fr` and `de`. It creates the workspace configuration
and initializes or migrates the SQLite database, but creates no records or accounts, so `doctor` is
healthy immediately afterwards. Use `repairflow init --interactive` to answer prompts for the
workspace, port, language and optional demo data. Initialization never overwrites an existing
configuration or existing records, and `--language` on an existing workspace reports that the
existing language was kept.

`repairflow serve` logs its actual `/login` URL, version, schema and workspace on stderr once the
listener is bound, then stops cleanly on Ctrl+C. If authentication is enabled and no active account
exists, it prints the `repairflow user add admin --role admin` remedy; the web login page has no
sign-up or account-listing page.

Passwords must be 6–256 characters. Use a long, unique password whenever the interface may be
reachable from another machine.

## Manage accounts

Accounts are managed through the CLI:

```bash
export REPAIRFLOW_NEW_PASSWORD='a-long-unique-password'
repairflow user add technician --role technician
unset REPAIRFLOW_NEW_PASSWORD

repairflow user list
export REPAIRFLOW_NEW_PASSWORD='a-new-long-unique-password'
repairflow user passwd technician
unset REPAIRFLOW_NEW_PASSWORD
repairflow user disable technician
repairflow user enable technician
```

- **admin:** all records, attachments, settings and attachment purge.
- **technician:** daily customer, repair, entry, part, estimate and attachment work; no purge.
- **viewer:** read-only browsing and downloads; can change its own password.

Password and account changes revoke the affected sessions. The last active admin cannot be disabled.

## Workspace and configuration

A workspace contains everything that belongs to one RepairFlow instance:

```text
config.toml           configuration
data/repairflow.db    SQLite database
files/                original attachments
backups/              reserved local backup directory
logs/                 service and diagnostic logs
cache/, run/          disposable runtime data
locales/              optional translation overrides
```

The configuration file is created with owner-only permissions. RepairFlow refuses a workspace that
is group- or world-writable. Do not edit the SQLite database directly or move individual files out
of `files/`; use the supported backup and restore commands instead.

## Next steps

- [Daily use](USAGE.md) for repair workflows, attachments and backups.
- [Deployment](DEPLOY.md) for remote access, services, upgrades and uninstalling.
- [Security](SECURITY.md) before storing sensitive customer data or enabling remote access.

## Troubleshooting

**Check the installation and workspace**

```bash
repairflow version
repairflow config check
repairflow status
repairflow doctor
```

`doctor` exits nonzero and identifies an unhealthy workspace, schema, database or attachment check.
Use `--debug` or `REPAIRFLOW_DEBUG=1` for additional diagnostics without logging passwords, session
tokens or customer payloads.

**Port already in use**

Stop the other process or change `web.port` in `<workspace>/config.toml`, then run
`repairflow config check` and restart `repairflow serve`.

**Permission refused**

Restore owner-only permissions, then validate again:

```bash
chmod 0700 /path/to/workspace
chmod 0600 /path/to/workspace/config.toml
repairflow config check --home /path/to/workspace
```

Do not weaken permission checks or hand-edit the database to bypass a failed health check.
