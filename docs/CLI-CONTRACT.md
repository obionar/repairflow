# CLI contract

Contract version: **2**.

Text output is for people. Use `--json` for automation. Add commands, fields and codes additively;
do not redefine existing behavior. JSON consumers should ignore unknown fields inside `data`.

## Invocation

```text
repairflow [--home PATH] [--json] [--debug] COMMAND [ARGUMENTS]
```

Global flags work before or after commands. Workspace precedence is:

1. `--home PATH`
2. `REPAIRFLOW_HOME`
3. `~/.repairflow`

Paths must be absolute after optional `~` or `~/` expansion. RepairFlow refuses relative,
symlink-root, foreign-owned, group/world-writable and explicitly protected paths.
`REPAIRFLOW_REFUSED_ROOTS` accepts an OS path-list of protected trees.

Mutating commands accept `--actor NAME`, defaulting to `$USER` and then `operator`. This is audit
attribution, not authentication. Customer and repair creation accept
`--idempotency-key KEY` (8–128 characters from `[A-Za-z0-9_.-]`). An identical retry replays the
original result; a changed payload conflicts. Validation failures do not consume the key.

`--debug` or `REPAIRFLOW_DEBUG=1` enables structured stderr diagnostics. It does not change stdout
or business behavior.

## Configuration

`config.toml` supports:

```text
config_version
app.environment
web.host
web.port
web.auth_required        default: true
web.allow_remote         default: false
web.allow_insecure_lan   default: false
ui.language              default: en
```

A non-loopback `web.host` requires `web.allow_remote=true`. The server also rejects non-loopback
`Host` headers unless remote access is allowed. `web.auth_required=false` is intended for local
automated tests and demos; on a non-loopback listener it additionally requires the unsafe
`web.allow_insecure_lan=true` testing exception.

## Workspace, diagnostics and demo

```text
init [--demo [--demo-user NAME]] [--language CODE]
init --interactive
config check
status
doctor
cleanup
serve
seed demo [--demo-user NAME]
demo reset --confirm
```

`init` creates owner-only directories and writes configuration only when absent. Domain commands
create and migrate the database explicitly. Newer schemas are refused.

`doctor` checks the workspace, schema, database integrity, records, demo marker and attachment
bytes. `cleanup` removes stale upload staging and cache only; it never removes business records,
attachments, configuration, backups or recovery data.

Demo seeding refuses existing business data. Reset requires unchanged synthetic content and keeps
users and sessions. `--demo-user` creates an optional admin using `REPAIRFLOW_NEW_PASSWORD`; it never
prints or resets an existing account password.

## Customers

```text
customer add --name NAME [--phone P] [--email E] [--address A] [--notes N]
             [--idempotency-key K]
customer list [--search TEXT] [--page N] [--per-page N] [--include-archived]
customer show ID
customer history ID
customer update ID [--name V] [--phone V] [--email V] [--address V] [--notes V]
                   [--revision R]
customer archive ID
customer unarchive ID
```

## Repairs and log entries

```text
repair add [--customer ID] [--device D | --device-type T] [--brand B] [--model M]
           [--serial S] --issue I [--notes N] [--idempotency-key K]
repair list [--search TEXT] [--stage S] [--customer ID] [--page N] [--per-page N]
            [--include-archived]
repair show ID [--include-deleted]
repair update ID [--customer ID | --no-customer] [--device V] [--device-type V]
                 [--brand V] [--model V] [--serial V] [--issue V] [--notes V]
                 [--revision R]
repair stage ID STAGE [--reopen]
repair entry ID --kind work|test|contact|handover|note --body TEXT
repair entry-edit ENTRY_ID [--kind K] [--body B]
repair entry-delete ENTRY_ID
repair entry-restore ENTRY_ID
repair pickup ID --at RFC3339|YYYY-MM-DD --by NAME [--notes N]
repair archive ID
repair unarchive ID
```

Omit `--customer` to create an unassigned repair. Assign or clear it later with `--customer ID` or
`--no-customer`. JSON uses `customer_id: 0` and `unassigned: true` while unassigned.

Stages are `new`, `in_progress`, `waiting_parts`, `lab_test`, `customer_test`, `repaired` and
`no_repair`. Allowed transitions are:

```text
new           → in_progress, waiting_parts, lab_test, no_repair
in_progress   → waiting_parts, lab_test, customer_test, repaired, no_repair
waiting_parts → in_progress, lab_test, no_repair
lab_test      → in_progress, waiting_parts, customer_test, repaired, no_repair
customer_test → in_progress, repaired, no_repair
repaired      → in_progress with --reopen
no_repair     → in_progress with --reopen
```

Lists are stable ID-descending pages, defaulting to 20 and capped at 100 items. Search wildcard
characters are treated literally. Updates may supply the content-derived `revision` returned by
`show`; a stale revision conflicts. Omitting it selects last-write-wins behavior.

`ticket` remains a one-release alias for `repair`. JSON always reports the canonical `repair ...`
command. Legacy `/tickets...` web paths redirect to `/repairs...` with HTTP 308.

## Parts and estimates

```text
repair part-add REPAIR_ID --description D --qty Q [--unit U] [--cost C] --price P [--notes N]
repair part-list REPAIR_ID [--include-deleted]
repair part-update PART_ID [--description D] [--qty Q] [--unit U] [--cost C]
                           [--clear-cost] [--price P] [--notes N] [--revision R]
repair part-remove PART_ID
repair part-restore PART_ID

estimate create REPAIR_ID [--note N] [--discount-amount C | --discount-percent P]
estimate list REPAIR_ID
estimate show ID
estimate accept ID
```

Money input accepts two decimal places and is stored as integer EUR cents. Quantities accept three
decimal places and are stored as integer milli-quantities. Parts soft-delete. Estimates are immutable
snapshots; accepting one supersedes the previous accepted version. Amount discounts above the
subtotal are refused.

## Attachments

```text
attachment add --repair ID --file PATH [--caption C] [--entry ENTRY_ID] [--source S]
attachment list --repair ID [--all]
attachment show ID
attachment export ID --out PATH
attachment caption ID --caption TEXT
attachment link ID --entry ENTRY_ID
attachment unlink ID --entry ENTRY_ID
attachment detach ID
attachment restore ID
attachment purge ID --confirm
```

Original bytes are streamed, SHA-256 hashed and exported unchanged. Export never overwrites. Entry
links must belong to the same repair. Detach is recoverable; purge requires a detached attachment,
confirmation and a 30-day retention period.

`preview_safe` means the content was sniffed as JPEG, PNG or WebP. `preview_inline` additionally
requires a size of at most 8 MiB. Web downloads use a SHA-256 ETag and private five-minute cache.

For one release, `attachment add` and `attachment list` also accept `--ticket ID` as an alias for
`--repair ID`.

## Accounts

```text
user add USERNAME --role admin|technician|viewer
user list
user passwd USERNAME
user enable USERNAME
user disable USERNAME
```

`user add` and `user passwd` read the new password from `REPAIRFLOW_NEW_PASSWORD`. Passwords must be
6–256 characters. Password and account changes revoke affected sessions. Disabling the last active
admin is refused.

## Backup and restore

```text
backup create --output ABSOLUTE_PATH
backup verify ARCHIVE
restore ARCHIVE --home PATH
```

Backup output must be absolute and outside the source workspace. Restore targets a new, empty
workspace only. Archives contain a consistent database snapshot, configuration and hash-verified
original attachments; they are not encrypted.

## Services and updates

```text
service install [--init systemd|openrc|none] [--name NAME] [--no-enable]
service status [--init systemd|openrc|none] [--name NAME]
service remove [--init systemd|openrc|none] [--name NAME]

update check --manifest MANIFEST --signature SIGNATURE
update apply --file ARTIFACT --manifest MANIFEST --signature SIGNATURE --confirm
update keygen --out DIR
update sign --key KEY --manifest MANIFEST --out SIGNATURE

version
help
```

Service installation supports systemd user units and OpenRC scripts. `--name` selects an independent
instance; units belonging to another instance are not overwritten or removed.

Updates use local files only and require Ed25519 signature and SHA-256 verification. The optional
`scripts/update-github.sh` downloads one exact public GitHub release tag into temporary storage and
then invokes these same local checks; it does not add an API, background polling or automatic restart.
`update apply` requires a signed release build with the trusted public key embedded. Regular-file,
platform, version, size and hash checks are mandatory. Same-version installs, downgrades, symlinks and
FIFOs are refused. Verified apply is Linux-only; restart through the service supervisor after success.

## JSON output

Success:

```json
{"ok":true,"command":"repair stage","contract":2,"data":{"id":6,"stage":"repaired"}}
```

Failure:

```json
{"ok":false,"command":"customer update","contract":2,"error":{"code":"conflict","message":"..."}}
```

Successful JSON goes to stdout. On failure, stdout is empty and the JSON error envelope goes to
stderr. Error codes and exit statuses are stable; human-readable messages may change.

## Exit codes

| Exit | Codes | Meaning |
|---:|---|---|
| 0 | — | Success |
| 1 | `internal_error` | Unexpected failure; retry with debug diagnostics |
| 2 | `usage_error`, `validation_error`, `unsafe_path`, `permission_refused` | Invalid input or refused path/permission |
| 3 | `workspace_exists`, `workspace_not_initialized`, `config_invalid`, `config_version_unsupported`, `conflict` | Workspace, configuration, schema or state conflict |
| 4 | `not_found` | Referenced record or resource is absent |

## Core JSON shapes

- List pages: `{items,page,per_page,total}`.
- `repair show`: repair fields plus `entries` and `activity`.
- `customer show`: `{customer,history}`.
- Attachment export: `{id,path,size_bytes,sha256}`.
- `status`: `{version,home,initialized,config_version,schema_version,customers,repairs,demo_seeded}`.

A customer includes `id`, `name`, `phone`, `email`, `address`, `notes`, optional `archived_at`,
`revision`, `created_at` and `updated_at`.

A repair includes `id`, `customer_id`, `unassigned` when applicable, `customer_name` when assigned,
`device`, `device_type`, `brand`, `model`, `serial`, `issue`, `reported_issue`, `stage`, `notes`,
pickup fields, optional `archived_at`, `revision`, `created_at` and `updated_at`. `device` is the
display alias derived from brand/model or device type.

Part, estimate and attachment fields follow their JSON struct tags in `internal/app`. Payment and
MCP commands are not implemented.
