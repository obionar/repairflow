# Daily use

RepairFlow keeps one shop workspace in SQLite plus original attachment files. The web interface and
CLI operate on the same records and enforce the same domain rules.

## A typical repair workflow

1. **Create or select a customer.** Store contact details, address and notes. Customer records can
   be archived when no longer active.
2. **Open a repair.** Record the device type, brand, model, serial number and reported issue;
   put all intake information in the issue field. A customer is optional at intake.
3. **Add log entries.** Use `work`, `test`, `contact`, `handover` or `note` entries to build a dated
   history. Editing an entry preserves its previous revisions; deleted entries can be restored.
4. **Add parts.** The web form uses a description, whole-piece quantity, optional cost and price.
   The CLI retains the versioned-estimate commands for compatibility; the web repair page does not
   expose estimates.
5. **Change the repair status.** A typical path is `new` → `in_progress` →
   `waiting_parts` → `lab_test` → `customer_test` → `repaired` or `no_repair`; the workflow does not
   require every stage, and [the CLI contract](CLI-CONTRACT.md) lists every allowed transition.
6. **Record collection.** Pickup is separate from repair status and the web records the collection
   time automatically. A repaired device can remain in
   the awaiting-pickup queue until handover.
7. **Archive when appropriate.** Archived customers and repairs retain their records and history.
   Archived repairs remain visible in the list with an archived badge.

Reopening `repaired` or `no_repair` work is explicit and audited. The dashboard shows open-work and
awaiting-pickup queues, including repairs without a customer; archived records do not appear in
those queues.

Repair log, activity, parts and gallery sections use bounded pages with previous/next links; older
records remain available without JavaScript. Rejected entry corrections and pickup submissions keep
the submitted fields on the repair page so you can fix and retry them. If an entry was deleted meanwhile, its rejected correction is available
as read-only text under deleted entries for copying; restore the entry before editing it. Drafts
are not saved to browser storage or across session expiry, so copy long unsaved notes before
signing in again.

### Repairs without a customer

On **Repairs → New repair**, leave the customer field blank to create an **Unassigned** repair.
Open it later and choose a customer to assign it. In the CLI:

```bash
repairflow repair add --device-type "Laptop" --issue "Does not power on"
repairflow repair update REPAIR_ID --customer CUSTOMER_ID
repairflow repair update REPAIR_ID --no-customer
```

JSON represents an unassigned repair with `customer_id: 0` and `unassigned: true`.

## Repair labels

**Print repair label** on a repair page prints one sticker with the repair number and the customer
name, so a device on the shelf can be matched back to its page. An unassigned repair prints the
repair number alone. Each click prints one label; nothing is printed automatically and a failed print
is never retried, because the label may already have reached paper. Confirmed prints appear in the
repair's activity trail.

The label printer is chosen by an administrator in **Settings → Label printer**. Only the Marklife
P15 is supported today (`p15`), through its own driver:

1. Install `p15` on the host that runs RepairFlow, for the same user, so `p15` is on `PATH` or at
   `/usr/local/bin/p15`. Run its daemon, so a warm link keeps the usual ~5 s print short.
2. Select `p15` in Settings and use **Print test label** to confirm the setup.

Printer-specific values stay in the p15 configuration (`p15 config get|set`: print density, label
size, printer name) so each setting has one authority. p15 takes exclusive control of one Bluetooth
adapter while it prints; use a dedicated adapter on a shared host. When the printer is unavailable,
repairs keep working and the print action reports the driver's reason.

What a failed print does, and does not, do:

- Nothing is recorded for a failed print, and it is never retried automatically — the label may
  already have reached the paper. Check the printer before pressing the button again.
- Without a running p15 daemon the first attempt pays the driver's discovery timeout (tens of
  seconds) before it reports; a warm daemon answers in seconds.
- A failed raw-HCI attempt can leave the Bluetooth adapter powered down. Check
  `bluetoothctl show` and power it back on (`bluetoothctl power on`) before using other devices on
  that adapter.

## Attachments

Add photos, test evidence or documents from the repair page, or with the CLI:

```bash
repairflow attachment add --repair REPAIR_ID --file /path/to/photo.jpg --caption "Before repair"
repairflow attachment list --repair REPAIR_ID
repairflow attachment export ATTACHMENT_ID --out /absolute/path/photo.jpg
```

RepairFlow stores the original bytes and their SHA-256 hash. Attachments can have captions and links
to one or more log entries on the same repair. Export never overwrites an existing file.

Detaching an attachment hides it from normal use but keeps it recoverable. Permanent purge requires
an admin, an explicit confirmation and a 30-day retention period. JPEG, PNG and WebP images up to
8 MiB can preview inline; other supported uploads are downloaded unchanged. The default upload
limit is 256 MiB per file.

## Parts

```bash
repairflow repair part-add REPAIR_ID --description "Battery" --qty 1 --price 49.90
```

The web form accepts a whole-piece quantity and always stores `pcs`. The CLI/API retain decimal
quantities and unit fields for existing data and scripts. Estimate commands remain in the CLI/API for
compatibility, but estimates are not exposed on the web repair page; they are snapshots, not invoices
or payment records. RepairFlow does not track stock levels, payments, deposits or refunds.

## CLI essentials

Run `repairflow help` for the complete built-in command list. Common commands include:

```bash
repairflow customer list
repairflow customer add --name "Customer Name" --phone "+357…"
repairflow repair list --stage in_progress
repairflow repair show REPAIR_ID
repairflow repair entry REPAIR_ID --kind work --body "Replaced power connector"
repairflow repair stage REPAIR_ID in_progress
```

Only valid stage transitions are accepted; for example, a `new` repair cannot jump directly to
`repaired`. Use `--json` for automation. Edits can use the `revision` returned by `show` to reject stale writes;
without it, updates use last-write-wins behavior. See [CLI contract](CLI-CONTRACT.md) for every
command, JSON field and exit code.

The workspace is selected in this order: `--home PATH`, `REPAIRFLOW_HOME`, then `~/.repairflow`.
Paths must be absolute after `~` expansion.

## Interface language

Choose a workspace language during `init --language CODE` or set `[ui] language` in `config.toml`.
Each browser can override it in **Settings → Language**. The override is stored in that browser, not
in the user account.

English, Greek, Russian and Turkish are complete. Spanish, French and German are partial and fall
back to English for missing strings. See [Translations](TRANSLATIONS.md) for local overrides and
contributing translations.

## Back up and restore

Create and verify backups regularly, and keep a copy on separate storage:

```bash
repairflow backup create --output /absolute/path/repairflow.rfbak
repairflow backup verify /absolute/path/repairflow.rfbak
```

A backup contains a consistent database snapshot, configuration when present, and original attachments.
Cache, logs, runtime files and local `locales/` translation overrides are excluded; copy overrides
separately if you use them. Backups are **not encrypted** and include customer data and
password hashes; protect them accordingly. `backup verify` checks the archive structure and hashes,
cross-checks declared sizes and schema against the contents, opens the database snapshot, checks
attachment rows, and validates any archived configuration. It uses the same checks as restore but
is not a substitute for a real restore rehearsal.

Restore only into a new, empty workspace:

```bash
repairflow restore /absolute/path/repairflow.rfbak --home /absolute/path/restored-workspace
repairflow doctor --home /absolute/path/restored-workspace
```

If a newer binary reports an outdated schema, stop the service and run
`repairflow init --backup /absolute/pre-upgrade.rfbak --home /absolute/workspace` after creating and
verifying that backup. Ordinary commands and `serve` refuse an outdated schema rather than migrating
silently. Restore verifies the archive, database and attachment references before publishing the workspace.
It never overwrites an existing workspace or a file that appears during publication. A publication
failure rolls back files from that restore attempt. Test recovery before you need it.

## Demo maintenance

The demo launcher is the safest way to explore RepairFlow because it always starts with a fresh
temporary workspace (`./demo/run-demo.sh` or `./scripts/run-demo.sh`). For a persistent synthetic
workspace, use `./demo/dev.sh`; it keeps `demo/workspace/` between runs. An existing proven demo
workspace can be reset only while its synthetic content is unchanged:

```bash
repairflow demo reset --confirm --home /absolute/path/demo-workspace
```

Reset preserves demo users and sessions. It refuses edited or real workspaces. Never seed demo data
into a real workspace.

## Health checks and cleanup

```bash
repairflow status
repairflow doctor
repairflow cleanup
```

- `status` summarizes the workspace and record counts.
- `doctor` checks configuration, schema, database integrity, and attachment metadata plus the stored
  bytes (it hash-verifies every retained attachment, so a large gallery takes longer). It does not
  edit business records, but it may create/tighten the SQLite file and resolve interrupted attachment
  deletion links before reporting health.
- `cleanup` removes stale upload staging and cache data only; it does not remove records,
  attachments, configuration, backups or recovery data.

Use `--debug` or `REPAIRFLOW_DEBUG=1` when diagnosing a failure. For services, remote access and
upgrades, see [Deployment](DEPLOY.md).
