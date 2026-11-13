# Daily use

RepairFlow keeps one shop workspace in SQLite plus original attachment files. The web interface and
CLI operate on the same records and enforce the same domain rules.

## A typical repair workflow

1. **Create or select a customer.** Store contact details, address and notes. Customer records can
   be archived when no longer active.
2. **Open a repair.** Record the device type, brand, model, serial number, reported issue and
   internal notes. A customer is optional at intake.
3. **Add log entries.** Use `work`, `test`, `contact`, `handover` or `note` entries to build a dated
   history. Editing an entry preserves its previous revisions; deleted entries can be restored.
4. **Add parts and estimates.** Part lines have a description, quantity, unit, optional cost and
   price. Creating an estimate snapshots the active part lines; accepting a new estimate supersedes
   the previously accepted version.
5. **Move the repair through its stages.** A typical path is `new` → `in_progress` →
   `waiting_parts` → `lab_test` → `customer_test` → `repaired` or `no_repair`; the workflow does not
   require every stage, and [the CLI contract](CLI-CONTRACT.md) lists every allowed transition.
6. **Record collection.** Pickup is separate from repair stage, so a repaired device can remain in
   the awaiting-pickup queue until handover.
7. **Archive when appropriate.** Archived customers and repairs retain their records and history.
   Archived repairs remain visible in the list with an archived badge.

Reopening `repaired` or `no_repair` work is explicit and audited. The dashboard shows open-work and
awaiting-pickup queues, including repairs without a customer; archived records do not appear in
those queues.

### Repairs without a customer

On **Repairs → New repair**, leave the customer field blank to create an **Unassigned** repair.
Open it later and choose a customer to assign it. In the CLI:

```bash
repairflow repair add --device-type "Laptop" --issue "Does not power on"
repairflow repair update REPAIR_ID --customer CUSTOMER_ID
repairflow repair update REPAIR_ID --no-customer
```

JSON represents an unassigned repair with `customer_id: 0` and `unassigned: true`.

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

## Parts and estimates

```bash
repairflow repair part-add REPAIR_ID --description "Battery" --qty 1 --price 49.90
repairflow estimate create REPAIR_ID --note "Initial estimate"
repairflow estimate list REPAIR_ID
repairflow estimate accept ESTIMATE_ID
```

Money input uses decimal EUR with at most two decimal places; quantities allow three decimals.
Estimates are immutable snapshots, not invoices or payment records. RepairFlow does not track stock
levels, payments, deposits or refunds.

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

A backup contains a consistent database snapshot, configuration and original attachments. Cache,
logs and runtime files are excluded. Backups are **not encrypted** and include customer data and
password hashes; protect them accordingly.

Restore only into a new, empty workspace:

```bash
repairflow restore /absolute/path/repairflow.rfbak --home /absolute/path/restored-workspace
repairflow doctor --home /absolute/path/restored-workspace
```

Restore verifies the archive, database and attachment references before publishing the workspace.
It never overwrites an existing workspace. Test recovery before you need it.

## Demo maintenance

The demo launcher is the safest way to explore RepairFlow because it always starts with a fresh
temporary workspace. An existing proven demo workspace can be reset only while its synthetic content
is unchanged:

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
- `doctor` checks configuration, schema, database integrity and attachment consistency.
- `cleanup` removes stale upload staging and cache data only; it does not remove records,
  attachments, configuration, backups or recovery data.

Use `--debug` or `REPAIRFLOW_DEBUG=1` when diagnosing a failure. For services, remote access and
upgrades, see [Deployment](DEPLOY.md).
