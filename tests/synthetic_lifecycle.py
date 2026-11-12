#!/usr/bin/env python3
"""Disposable synthetic RepairFlow lifecycle smoke test (no production paths)."""
import json
import os
import pathlib
import shutil
import subprocess
import sys

BIN = sys.argv[1]
ROOT = pathlib.Path(sys.argv[2])
HOME = ROOT / "ws"
RESTORE = ROOT / "restored"
ARCHIVE = ROOT / "repairflow-test.rfbak"
EXPORT = ROOT / "exported-photo.jpg"


def run(*args, env=None, json_out=False, home=None):
    cmd = [BIN, *args, "--home", str(home or HOME)]
    if json_out:
        cmd.insert(1, "--json")
    proc = subprocess.run(cmd, text=True, capture_output=True,
                          env={**os.environ, **(env or {})})
    if proc.returncode != 0:
        raise RuntimeError(f"failed ({proc.returncode}): {' '.join(cmd)}\nstdout={proc.stdout}\nstderr={proc.stderr}")
    if json_out:
        return json.loads(proc.stdout)
    return proc.stdout


def expect(*args, code=0, env=None):
    proc = subprocess.run([BIN, *args, "--home", str(HOME)], text=True,
                          capture_output=True, env={**os.environ, **(env or {})})
    if proc.returncode != code:
        raise RuntimeError(f"expected exit {code}, got {proc.returncode}: {' '.join(args)}\n{proc.stderr}")


def main():
    ROOT.mkdir(parents=True)
    run("init")
    run("user", "add", "admin", "--role", "admin", env={"REPAIRFLOW_NEW_PASSWORD": "password12345"})
    customers = []
    for name, phone in [
        ("Maya Chen", "+357 99 100001"),
        ("Daniel Okafor", "+357 99 100002"),
        ("Sofia Rossi", "+357 99 100003"),
        ("Ahmed Hassan", "+357 99 100004"),
        ("Lena Fischer", "+357 99 100005"),
    ]:
        customers.append(run("customer", "add", "--name", name, "--phone", phone, json_out=True)["data"]["id"])

    devices = [
        ("Kenwood amplifier KA-501", "No sound on the left channel"),
        ("Dell XPS 13 laptop", "Does not power on"),
        ("Boss DS-1 effects pedal", "Intermittent bypass switch"),
        ("Behringer audio mixer", "Channel 3 distorted"),
        ("iPhone 12", "Cracked screen"),
    ]
    tickets = []
    for customer, (device, issue) in zip(customers, devices):
        tickets.append(run("ticket", "add", "--customer", str(customer), "--device", device,
                           "--issue", issue, json_out=True)["data"]["id"])

    # Stages and log entries.
    for ticket, stage in zip(tickets, ["in_progress", "in_progress", "waiting_parts", "lab_test", "in_progress"]):
        run("ticket", "stage", str(ticket), stage)
    run("ticket", "stage", str(tickets[4]), "no_repair")
    run("ticket", "stage", str(tickets[0]), "repaired")
    run("ticket", "pickup", str(tickets[0]), "--at", "2026-11-11", "--by", "Maya Chen", "--notes", "synthetic pickup")
    run("ticket", "entry", str(tickets[1]), "--kind", "work", "--body", "Opened laptop and inspected the power circuit")
    run("ticket", "entry", str(tickets[1]), "--kind", "test", "--body", "Measured 19V at the DC-in connector")
    run("ticket", "entry-edit", "1", "--body", "Opened laptop and inspected the charging circuit")
    run("ticket", "entry-delete", "2")
    run("ticket", "entry-restore", "2")

    # Parts, immutable estimates, discounts.
    run("ticket", "part-add", str(tickets[1]), "--description", "DC jack", "--qty", "1", "--cost", "4.50", "--price", "12.00")
    run("ticket", "part-add", str(tickets[1]), "--description", "Thermal fuse", "--qty", "1.5", "--unit", "pcs", "--price", "8.00")
    run("ticket", "part-update", "2", "--qty", "2", "--price", "9.00", "--revision", run("ticket", "part-list", str(tickets[1]), json_out=True)["data"][1]["revision"])
    estimate = run("estimate", "create", str(tickets[1]), "--discount-percent", "10", json_out=True)["data"]
    run("estimate", "accept", str(estimate["id"]))
    run("ticket", "part-remove", "1")
    run("ticket", "part-restore", "1")

    # Synthetic media and arbitrary files, including Unicode names.
    photo = ROOT / "intake ünïcode.jpg"
    photo.write_bytes(b"\xff\xd8\xff\xe0" + b"synthetic-jpeg" * 20)
    firmware = ROOT / "firmware dump.bin"
    firmware.write_bytes(bytes(range(256)) * 8)
    document = ROOT / "service notes.pdf"
    document.write_bytes(b"%PDF-1.4\n%synthetic service notes\n")
    csv = ROOT / "measurements.csv"
    csv.write_text("channel,voltage\nleft,19.0\nright,19.1\n")
    run("attachment", "add", "--ticket", str(tickets[1]), "--file", str(photo), "--caption", "intake photo", "--entry", "1")
    run("attachment", "add", "--ticket", str(tickets[1]), "--file", str(firmware), "--caption", "firmware backup")
    run("attachment", "add", "--ticket", str(tickets[1]), "--file", str(document), "--caption", "service notes")
    run("attachment", "add", "--ticket", str(tickets[1]), "--file", str(csv), "--caption", "measurements")
    run("attachment", "link", "1", "--entry", "2")
    run("attachment", "unlink", "1", "--entry", "2")
    run("attachment", "export", "1", "--out", str(EXPORT))
    assert EXPORT.read_bytes() == photo.read_bytes()
    run("attachment", "detach", "2")
    run("attachment", "restore", "2")

    # Search/list and JSON shapes.
    listed = run("ticket", "list", "--search", "laptop", json_out=True)
    assert listed["data"]["total"] == 1, listed
    assert run("ticket", "show", str(tickets[1]), json_out=True)["data"]["device"] == "Dell XPS 13 laptop"
    assert run("attachment", "list", "--ticket", str(tickets[1]), json_out=True)["data"][0]["caption"] == "intake photo"
    run("doctor")
    run("status")

    # Verified backup and portable restore into a separate empty workspace.
    run("backup", "create", "--output", str(ARCHIVE))
    run("backup", "verify", str(ARCHIVE))
    restored = run("restore", str(ARCHIVE), json_out=True, home=RESTORE)
    assert RESTORE.exists()
    original_db = HOME / "data" / "repairflow.db"
    restored_db = RESTORE / "data" / "repairflow.db"
    assert original_db.exists() and restored_db.exists()
    assert restored_db.stat().st_size > 0
    restored_photo = RESTORE / "files" / "tickets" / str(tickets[1])
    assert any(restored_photo.iterdir()), "restored attachment bytes missing"
    proc = subprocess.run([BIN, "doctor", "--home", str(RESTORE)], text=True, capture_output=True)
    assert proc.returncode == 0, proc.stderr
    proc = subprocess.run([BIN, "ticket", "show", str(tickets[1]), "--home", str(RESTORE), "--json"], text=True, capture_output=True)
    assert proc.returncode == 0 and "Dell XPS 13 laptop" in proc.stdout, proc.stderr
    print("PASS: synthetic lifecycle, JSON/text CLI, attachments, estimates, backup/restore")


if __name__ == "__main__":
    main()
