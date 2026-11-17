# Security

RepairFlow is designed for a local or privately accessed workshop workspace, not as a public
internet service or a multi-tenant platform. It is pre-release software; review the limits below
before storing sensitive customer data.

## Default protections

### Workspace and input

- Workspaces require safe absolute paths, acceptable ownership and owner-only permissions.
- Initialization does not overwrite existing configuration or records.
- Configuration and CLI parsing are strict. Text inputs and uploads are bounded.
- Database access uses parameterized SQL, foreign keys and transactions. User filenames never become
  storage paths.
- Newer database and configuration versions are refused rather than opened with incompatible
  behavior.

### Accounts and browser sessions

- Passwords are hashed with Argon2id. New passwords must be 6–256 characters; use a long, unique
  password for any remotely reachable instance.
- Session tokens are hashed, expiring and revocable. Password and account-state changes revoke
  affected sessions.
- Repeated login failures are throttled, with a 15-minute lockout after five failures. The bounded
  throttle evicts only expired or non-locked entries under capacity pressure; flooding cannot clear
  an active account lockout. At most two password hashes run concurrently; excess login attempts are
  refused with HTTP 429 before the expensive Argon2 work starts.
- Request bodies are capped before form parsing: 64 KiB for login, 1 MiB for ordinary forms, and the
  256 MiB attachment limit plus 1 MiB for multipart overhead. Oversized requests receive HTTP 413.
  Per-request read deadlines are 15 seconds for login, 30 seconds for ordinary forms and five
  minutes for attachment uploads.
- Admin, technician and viewer permissions are enforced on the server.
- Browser mutations use per-session CSRF tokens plus origin or same-origin checks.
- Cookies are `HttpOnly` and `SameSite=Strict`. Responses include content-security, frame, referrer
  and MIME-sniffing protections.
- The listener and accepted `Host` headers default to loopback. Remote access must be configured
  explicitly.

### Attachments, backups and updates

- Uploads are streamed under a size limit, stored under opaque names and SHA-256 hashed.
- Only content-sniffed JPEG, PNG and WebP images up to 8 MiB are previewed inline; other files download.
- Permanent attachment purge requires admin confirmation and a 30-day retention period. Recovery
  links protect original bytes across the database commit.
- Backup verification and restore share structure, size and hash checks, validate any archived
  configuration, and check the database's integrity, declared schema and attachment sizes/hashes.
  A restore into a disposable workspace remains the full recovery rehearsal. Restore never
  overwrites an existing workspace.
- Signed updates require Ed25519 and SHA-256 verification, regular files, platform and version
  checks, staged/published readback and a health check. Symlinks, FIFOs, unsigned builds, same-version
  installs and downgrades are refused. Updates and source installs share a lock next to the
  executable. Updates re-read the installed version under that lock before applying version policy.
  Replacing a root-owned `/usr/local/bin/repairflow` requires `sudo`/`doas`; the default
  `~/.local/bin/repairflow` belongs to the operator and needs neither.
- `SHA256SUMS` ships with a detached Ed25519 signature (`SHA256SUMS.sig`) because `manifest.json`
  covers only the binaries: the signature authenticates `LICENSE` and the operator update scripts in
  the archive. Verify the list before extracting or installing; an installed binary can do that with
  `update verify`. The first download still relies on the release host and the published key
  fingerprint.
- `GET /about` is public and shows the build version, commit, licence and the source URL compiled
  into the build (`-X repairflow/internal/buildinfo.SourceURL=...`). Operators who distribute a
  modified build must point that URL at the Corresponding Source of their version.
- The application makes no outbound network requests during ordinary use. The optional
  `scripts/repairflow-update` (or its `update-github.sh` wrapper) accesses GitHub only when invoked, extracts only the
  expected files from the platform archive, and still requires the installed binary to verify the
  signed manifest and hash before applying an update. When it needs privilege for the local
  replacement, automatic escalation requires a regular, non-symlink root-owned binary and
  root-owned parent directories without group/world write permission. It refuses unsafe paths
  rather than executing them as root; ownership/mode checks require `stat`.
- Label printing (off by default) runs one fixed local command, `p15`, with a repair number and
  customer name as arguments: no shell, a bounded timeout, inherited environment only. Only an
  administrator selects the printer and only a technician prints. p15 itself needs
  `cap_net_raw,cap_net_admin` for Bluetooth and is installed by the host operator; RepairFlow never
  takes those capabilities, and a failed print is reported rather than retried automatically.

## Trust boundaries

**The operating-system account that owns the workspace is the real security boundary.** A process
running as that account can read or modify the database and attachments directly, bypassing web
roles. Web roles are cooperative application permissions, not a sandbox between OS processes.

The default executable is `~/.local/bin/repairflow`, owned by the operator, so installing and
updating it needs no privilege. `REPAIRFLOW_PREFIX=/usr/local` installs a root-owned binary that a
regular user can execute but not replace; the installer then uses `doas`/`sudo` only for the local
replacement step. Workspaces remain owned by the
operator at the default `~/.repairflow` path or an explicit `--home`/`REPAIRFLOW_HOME`.

CLI `--actor` and `$USER` are self-asserted audit labels, not authentication. Web audit actors come
from authenticated sessions.

RepairFlow does not provide encryption at rest or isolation between mutually untrusted customers or
workshops. Use OS account separation, filesystem permissions and full-disk or storage encryption
where required.

## Remote access

The application serves plain HTTP. Prefer an SSH tunnel or a private TLS-terminating Tailscale proxy
in front of the loopback listener; see [Deployment](DEPLOY.md).

When Tailscale proxies requests:

- Keep RepairFlow bound to `127.0.0.1`; do not open a LAN listener as a substitute for the tunnel.
- `web.allow_remote=true` is required for the proxy's external `Host` header.
- Keep `web.auth_required=true`; the tailnet is an additional access layer, not a replacement for
  application login.
- All requests appear to come from `127.0.0.1`, so they share one login-throttle bucket.
- Session cookies are not marked `Secure` because RepairFlow cannot detect TLS termination by the
  local proxy.

Do not expose RepairFlow directly to the public internet.

## Unauthenticated LAN testing mode

Setting `web.auth_required=false` on a non-loopback listener requires the explicit
`web.allow_insecure_lan=true` exception. This mode has **no login, no CSRF token and no TLS**. Anyone
who can reach the port can read and modify every record.

Use it only temporarily with synthetic data on a trusted test network. Restore authentication and
disable the exception before adding real records or enabling any remote path.

## Sensitive data and diagnostics

Backups contain customer records, original attachments and password hashes. They are not encrypted
by RepairFlow. Store them on separate, access-controlled storage and protect copies during transfer.

Debug logging is opt-in through `--debug` or `REPAIRFLOW_DEBUG=1`. Logs exclude passwords, cookies,
CSRF and session tokens, authorization headers, query strings, customer payloads and file contents.
Web clients receive generic internal-error responses; detailed causes remain in local diagnostics.

## Reporting a vulnerability

Use **GitHub Private Vulnerability Reporting** on the repository's **Security** tab. If the feature
is not enabled, do not post exploit details in public issues. Never attach real customer records,
passwords, keys, session tokens or private workspace data to a report, and do not test against
another operator's workspace or installed service.
