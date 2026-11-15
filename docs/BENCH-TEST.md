# Workshop bench testing

Use a separate executable and a disposable synthetic workspace to evaluate RepairFlow on the actual
workshop computer, phone and printer. This is not a production installation or an upgrade procedure.
Keep the installed service, executable and real customer records untouched.

## Prepare a local build

From a source checkout, run the [development checks](DEVELOPMENT.md#verification), then:

```bash
make cross VERSION=0.2.0-bench.1
```

Use `dist/repairflow-linux-amd64` on an x86-64 Linux computer or
`dist/repairflow-linux-arm64` on a 64-bit Raspberry Pi. Cross-compilation is not a hardware test.
Record the binary's `version --json` output and checksum so observations identify the exact build.

These local builds have no release-signing public key. Signed `update apply` is intentionally
disabled; do not disable signature checks or install this build over a signed production binary.
Do not publish the build as an official release.

## Start an isolated workspace

On the bench machine, select the executable explicitly:

```bash
BIN=/absolute/path/to/repairflow-linux-amd64  # use the arm64 executable on a Pi
BASE=$(mktemp -d)
WS="$BASE/workspace"
REPAIRFLOW_NEW_PASSWORD=bench-demo-only "$BIN" --home "$WS" init --demo --demo-user bench
```

Edit **only this disposable** `$WS/config.toml`: set `[web] port = 8094`, or another unused port.
Keep `host = "127.0.0.1"` and authentication enabled. Then:

```bash
"$BIN" --home "$WS" config check
"$BIN" --home "$WS" doctor
"$BIN" --home "$WS" version --json
"$BIN" --home "$WS" serve
```

Open <http://127.0.0.1:8094/login> and sign in with `bench` / `bench-demo-only`.
The password and dataset are public/synthetic, never suitable for real work. Ctrl+C stops the
foreground server; the workspace remains so you can inspect or back up the test results.

For another device, use the private HTTPS/tunnel procedures in [Deployment](DEPLOY.md).
Use a distinct proxy endpoint for the bench instance; do not repoint the production proxy or
turn off authentication. Do not expose the listener directly to the internet.

## Exercise the actual work

- Create a synthetic customer, including address/contact details, then create a repair.
- Create an unassigned repair; assign and clear its customer later. Try duplicate customer names.
  If an older customer is absent from suggestions, open that customer's page and create its repair
  from there; do not guess a matching name.
- Add work/test/contact entries; correct one and inspect its revision history. Delete/restore an
  entry. Submit an invalid correction and confirm that its text remains available for retry.
- Add/edit/remove/restore a whole-piece part. Verify that unknown cost stays distinct from zero.
- Upload a synthetic photo/document, edit its caption, link it to an entry, download it and compare
  the original bytes. Detach/restore it. Do not bypass the purge grace period to test deletion.
- Move through repair stages; confirm terminal work requires explicit reopening.
- Record pickup, then archive/unarchive the repair. Check list and dashboard behavior.
- Check keyboard navigation and the actual phone width. Repeat the core workflow without JavaScript.
- Try the intended interface language, especially warnings and destructive actions.

### P15 labels

The host operator must already have installed/configured `p15` and its Bluetooth permissions for
the same user. Use test label stock. Select the printer in Settings, print one test label, then
print a repair label. Check the repair number/customer name and the activity record. Test an
unassigned repair and an unavailable printer. Each click is a physical print; do not retry a failed
print until checking whether paper already came out. RepairFlow supplies the number and name;
label layout/size remain in p15.

RepairFlow must remain usable when printing is switched off or the driver is unavailable.

Record three things on the bench machine as well: how long the button takes to answer (a cold,
daemon-less p15 pays its whole discovery timeout), whether `bluetoothctl show` still reports the
adapter powered after a failed attempt, and the text the page shows — it is the driver's own
message, not a user-facing sentence.

## Rehearse recovery on the bench machine

Stop the foreground server with Ctrl+C, then use new archive/destination paths:

```bash
"$BIN" --home "$WS" backup create --output "$BASE/bench.rfbak"
"$BIN" backup verify "$BASE/bench.rfbak"
"$BIN" restore "$BASE/bench.rfbak" --home "$BASE/restored"
"$BIN" --home "$BASE/restored" doctor
"$BIN" --home "$BASE/restored" serve
```

Log into the restored workspace and check representative repairs, entry revisions, parts, detached
attachments and the printer setting. Download/export the same synthetic originals and compare their
bytes. Stop the original server first: the restored config keeps its port. Verification alone is
not proof of recovery; record how long restoration and reopening actually take on this machine.

Local `locales/` overrides are not in the archive; copy them separately if testing custom wording.
Do not use `demo reset` to discard edited bench work: its pristine-data guard deliberately refuses.

## Capture useful feedback

Record the build version/commit/checksum, target architecture, browser, printer/label size, steps,
expected result and actual result. Include timings for slow pages/printing/restore and a screenshot
using synthetic data only. Keep workspace databases, photos, credentials and logs out of Git.

A successful synthetic bench run is not authorization to migrate real records or publish a release.
Before a real upgrade, follow [Deployment](DEPLOY.md#upgrade): verified backup, stop old writers,
retain the old binary and its matching pre-upgrade backup, then demonstrate restart and recovery.
An executable-only rollback cannot undo a schema migration.
