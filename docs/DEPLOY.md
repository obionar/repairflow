# Deploy on another Linux machine

A RepairFlow deployment needs one static binary, one workspace and, optionally, one service unit.
There is no database server, application runtime or outbound network dependency.

## Target requirements

- Linux x86-64 (`amd64`) or ARM64 (`arm64`).
- An operator account that owns the binary and workspace.
- Enough local storage for the binary, database, attachments and backups.
- `systemd` for a user service, or OpenRC for a system service. Foreground operation needs neither.

Use separate workspaces and ports for separate instances. RepairFlow does not synchronize or share
a workspace between machines.

## Copy and install

Copy the archive for the target architecture and `SHA256SUMS` to the machine:

```bash
scp repairflow_Linux_x86_64.tar.gz SHA256SUMS technician@host:~/
```

Then verify, extract and install exactly as
[Install and first run](INSTALL.md#install-the-executable) describes, and keep `LICENSE`,
`manifest.json` and `manifest.sig` from the archive with the installation.
Use the same guide to initialize a workspace and create the first admin; to move existing records
instead, use [Move a workspace](#move-a-workspace) below.

## Reach the web interface remotely

RepairFlow serves plain HTTP and listens on `127.0.0.1:8091` by default. Keep authentication enabled
and do not expose that listener directly to an untrusted network.

### SSH tunnel

For occasional or emergency access, tunnel the loopback port over SSH:

```bash
ssh -L 8091:127.0.0.1:8091 USER@HOST
```

Then open <http://127.0.0.1:8091/login> on the local machine. SSH must permit local TCP forwarding.
No additional proxy daemon is required.

### Tailscale HTTPS proxy

For persistent private access, Tailscale can terminate HTTPS and proxy to the loopback listener.
The host and each client must join the tailnet, and HTTPS certificates must be enabled for it.

On an Alpine/OpenRC host:

```bash
doas apk add tailscale tailscale-openrc
doas rc-update add tailscale default
doas rc-service tailscale start
doas tailscale up --hostname=repairflow --accept-dns=false
doas tailscale serve --bg --https=443 http://127.0.0.1:8091
doas tailscale serve status
```

Follow the login or consent URL printed by Tailscale. Disable key expiry for an unattended host.
Enabling HTTPS certificates publishes the machine and tailnet names in Certificate Transparency
logs, so do not put sensitive information in either name.

The proxy preserves the external `Host` header. Allow that header while keeping RepairFlow bound to
loopback:

```toml
# <workspace>/config.toml
[web]
host = "127.0.0.1"
allow_remote = true
auth_required = true
```

Validate and restart the service:

```bash
repairflow config check
# systemd: systemctl --user restart repairflow.service
# OpenRC:  doas rc-service repairflow restart
```

Open the shown `https://HOST.TAILNET.ts.net/` address from another tailnet device. The LAN address
should not expose port 8091.

Two application limits apply behind the proxy — one shared login-throttle bucket and a session cookie
that cannot be marked `Secure`; see [Security](SECURITY.md#remote-access). Tailscale provides the outer
network access control; RepairFlow authentication remains mandatory.

### Unsafe LAN demo mode

`web.allow_insecure_lan=true` permits `auth_required=false` on a non-loopback listener for temporary
UI testing only. It has no login, no CSRF token and no TLS. Read
[Security](SECURITY.md#unauthenticated-lan-testing-mode) before using it; never use it with real
records or remote access enabled.

## Run as a service

Install the binary at its final path and initialize the workspace before installing a service.

### systemd user service

```bash
repairflow service install
repairflow service status
journalctl --user -u repairflow.service
```

This creates `~/.config/systemd/user/repairflow.service` and attempts to enable and start it.
`--no-enable` writes the unit without starting it. If no user session is available, enable it later:

```bash
systemctl --user enable --now repairflow.service
```

A user service normally stops when the user's last session ends. Configure lingering with
`loginctl enable-linger USER` if it must run without an active login.

### Alpine / OpenRC service

```bash
doas repairflow service install --init openrc
rc-service repairflow status
```

The installer writes `/etc/init.d/repairflow`, runs the process as the workspace owner and logs to
`<workspace>/logs/service.log`. Pass `--home /absolute/workspace` when the workspace is not the
invoking user's default. Without sufficient privileges, the installer stages the script under
`<workspace>/cache/service/` and prints the privileged command needed to finish installation.

Use `--init systemd`, `--init openrc` or `--init none` to override automatic detection. `none`
writes no unit and prints the command for another supervisor.

### Named instances

`--name NAME` selects an independent service instance:

```bash
repairflow service install --name lab --home /absolute/lab-workspace
repairflow service status --name lab
repairflow service remove --name lab
```

Use a separate workspace, port, stable binary path and unit for each instance. RepairFlow refuses to
overwrite or remove a unit marked as belonging to another instance. Service removal never deletes
workspace data.

## Move a workspace

On the old machine:

```bash
repairflow backup create --output /absolute/path/handover.rfbak --home /path/to/source
repairflow backup verify /absolute/path/handover.rfbak
```

Copy the archive to separate storage, then on the new machine:

```bash
repairflow restore /path/to/handover.rfbak --home /absolute/path/new-workspace
repairflow doctor --home /absolute/path/new-workspace
```

Restore requires a new, empty destination and never overwrites existing records. The archive
contains customer data, attachments and password hashes and is not encrypted.

## Upgrade

Always create and verify a backup first.

### Signed GitHub update

After a verified backup, `update-github.sh` downloads the installable archive for the exact release
tag from the public `obionar/repairflow` repository. It selects the current Linux architecture,
extracts the signed manifest and binary, verifies the signature plus artifact SHA-256/size, and does
not change the installation by default:

```bash
./update-github.sh --tag X.Y.Z --binary "$HOME/.local/bin/repairflow"
```

Apply only the reviewed candidate:

```bash
./update-github.sh --tag X.Y.Z --binary "$HOME/.local/bin/repairflow" --apply
```

The script requires `wget` or `curl`, `tar` and `sha256sum`. It performs network access only when
invoked, uses no GitHub API or `jq`, and removes temporary downloads on success or failure. `--apply`
reuses the installed binary's signed update path and preserves `<executable>.prev`. Restart and
verify the service separately.

### Signed offline update

A signed release archive contains `manifest.json`, `manifest.sig` and the `repairflow` binary:

```bash
tar -xzf repairflow_Linux_x86_64.tar.gz
repairflow update check --manifest manifest.json --signature manifest.sig
repairflow update apply --file repairflow \
  --manifest manifest.json --signature manifest.sig --confirm
```

The installed binary must contain the matching release public key. Development and unsigned builds
refuse `update apply`. RepairFlow verifies the signature, platform, version, file size and SHA-256
before replacing the executable and retains the previous binary as `<executable>.prev`.

Restart the service after a successful update, then check:

```bash
repairflow version
repairflow doctor
```

Verified apply is supported on Linux. Same-version installs and downgrades are refused. The running
process is not replaced in memory; the supervisor performs the restart.

### Manual binary replacement

Stop the service and replace the executable. If the executable path changed, rerun
`repairflow service install` **before** starting the service so the unit uses the new path:

```bash
# systemd
systemctl --user stop repairflow.service
install -Dm0755 repairflow ~/.local/bin/repairflow
# If the path changed: repairflow service install --no-enable
systemctl --user start repairflow.service

# OpenRC equivalents: doas rc-service repairflow stop|start
repairflow version
repairflow doctor
```

Schema migrations run forward-only when the workspace opens; a newer workspace is refused rather
than downgraded.

## Uninstall

```bash
repairflow service remove
repairflow backup create --output /absolute/path/final-backup.rfbak
repairflow backup verify /absolute/path/final-backup.rfbak
rm ~/.local/bin/repairflow
```

This removes the service and executable but leaves the workspace intact. Delete the workspace only
when you no longer need its database, configuration and original attachments.
