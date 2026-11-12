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

### From a release bundle

Choose `repairflow-linux-amd64` or `repairflow-linux-arm64`, then verify the downloaded files before
installing:

```bash
sha256sum -c SHA256SUMS --ignore-missing
install -Dm0755 repairflow-linux-amd64 ~/.local/bin/repairflow
~/.local/bin/repairflow version
```

Use the arm64 filename instead on an ARM64 machine. Keep the executable at a stable path: service
units and offline updates refer to that location.

### From source

```bash
make build
install -Dm0755 repairflow ~/.local/bin/repairflow
~/.local/bin/repairflow version
```

If `~/.local/bin` is not on `PATH`, call `~/.local/bin/repairflow` directly or add this to your
shell profile:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

The examples below use `repairflow` for readability.

## Try the disposable demo

From either a source checkout or a release bundle:

```bash
./run-demo.sh                 # release bundle
./scripts/run-demo.sh         # source checkout
```

Open <http://127.0.0.1:8091/login> and use `demo` / `repairflow`. The launcher creates a temporary
workspace filled with synthetic customers, repairs, entries, parts, estimates and attachments.
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

`init` supports `en`, `el`, `ru`, `tr`, `es`, `fr` and `de`. Use `repairflow init --interactive`
to answer prompts for the workspace, port, language and optional demo data. Initialization never
overwrites an existing configuration or existing records.

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
