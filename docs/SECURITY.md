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
  an active account lockout.
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
- Backup creation and restore verify database and attachment integrity. Restore never overwrites an
  existing workspace.
- Signed updates require Ed25519 and SHA-256 verification, regular files, platform and
  version checks, staged/published readback and a health check. Symlinks, FIFOs, unsigned builds,
  same-version installs and downgrades are refused.
- The application makes no outbound network requests during ordinary use. The optional
  `scripts/update-github.sh` accesses GitHub only when the operator invokes it, extracts only the
  expected files from the platform archive, and still requires the installed binary to verify the
  signed manifest and hash before applying an update.

## Trust boundaries

**The operating-system account that owns the workspace is the real security boundary.** A process
running as that account can read or modify the database and attachments directly, bypassing web
roles. Web roles are cooperative application permissions, not a sandbox between OS processes.

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
