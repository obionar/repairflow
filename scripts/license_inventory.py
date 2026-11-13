#!/usr/bin/env python3
"""Check RepairFlow's third-party license inventory.

The runtime report covers packages compiled into ./cmd/repairflow for both shipped
Linux architectures. The separate browser-test module is reported for development
inventory only; its dependencies are not shipped in RepairFlow binaries.

No notice document is generated: the check verifies that every compiled dependency
carries a known license and that first-party assets are reviewed. Reports land in
dist/licenses/ (ignored by git).
"""
from __future__ import annotations

import argparse
import csv
from dataclasses import dataclass
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
REVIEWED_ASSETS = ROOT / "licenses" / "first-party-assets.txt"
OUTPUT_DIR = ROOT / "dist" / "licenses"
LEGAL_FILE = re.compile(
    r"^(?:LICENSE|LICENCE|COPYING|NOTICE|PATENTS|AUTHORS|CONTRIBUTORS)(?:[._-].*)?$",
    re.IGNORECASE,
)
ASSET_SUFFIXES = {
    ".css", ".js", ".svg", ".png", ".jpg", ".jpeg", ".webp", ".gif", ".ico",
    ".woff", ".woff2", ".ttf", ".otf", ".mp3", ".mp4", ".pdf", ".bin",
}


@dataclass(frozen=True)
class ModuleLicense:
    module: str
    url: str
    license: str


@dataclass(frozen=True)
class LicenseOverride:
    version: str
    detected_license: str
    detected_url: str
    license: str
    url: str


# go-licenses 1.6.0 either misses these root licenses or selects a nested
# third-party file instead. Each correction is valid only for the exact reviewed
# upstream version and raw detection; upgrades must be reviewed explicitly.
LICENSE_OVERRIDES = {
    "modernc.org/libc": LicenseOverride(
        "v1.80.1", "MIT",
        "https://gitlab.com/cznic/libc/blob/v1.80.1/LICENSE-3RD-PARTY.md",
        "BSD-3-Clause", "https://gitlab.com/cznic/libc/blob/v1.80.1/LICENSE",
    ),
    "modernc.org/mathutil": LicenseOverride(
        "v1.7.1", "Unknown", "Unknown",
        "BSD-3-Clause", "https://gitlab.com/cznic/mathutil/blob/v1.7.1/LICENSE",
    ),
    "modernc.org/memory": LicenseOverride(
        "v1.12.1", "BSD-3-Clause",
        "https://gitlab.com/cznic/memory/blob/v1.12.1/LICENSE-GO",
        "BSD-3-Clause", "https://gitlab.com/cznic/memory/blob/v1.12.1/LICENSE",
    ),
}


def containing_module(package: str, versions: dict[str, str]) -> str:
    candidates = [
        module for module in versions
        if package == module or package.startswith(module + "/")
    ]
    if not candidates:
        raise RuntimeError(f"reported package has no containing module: {package}")
    return max(candidates, key=len)


def parse_report(text: str, versions: dict[str, str] | None = None) -> dict[str, ModuleLicense]:
    """Parse go-licenses CSV, normalize packages to modules and merge platform rows."""
    result: dict[str, ModuleLicense] = {}
    versions = versions or {}
    for row in csv.reader(text.splitlines()):
        if not row:
            continue
        if len(row) != 3:
            raise RuntimeError(f"unexpected go-licenses row: {row!r}")
        package, url, license_name = row
        module = containing_module(package, versions) if versions else package
        version = versions.get(module, "")
        if module in LICENSE_OVERRIDES:
            override = LICENSE_OVERRIDES[module]
            if (
                version != override.version
                or license_name != override.detected_license
                or url != override.detected_url
            ):
                raise RuntimeError(
                    f"license override mismatch for {module}@{version}; "
                    "review the upstream license and source URL"
                )
            license_name, url = override.license, override.url
        if license_name == "Unknown" or not license_name:
            raise RuntimeError(f"unknown module license requires review: {module}")
        if url == "Unknown" or not url:
            url = f"https://pkg.go.dev/{module}"
        entry = ModuleLicense(module, url, license_name)
        previous = result.get(module)
        if previous:
            if previous.license != entry.license:
                raise RuntimeError(f"conflicting licenses reported for {module}")
            if previous.url != entry.url:
                raise RuntimeError(f"conflicting license source URLs reported for {module}")
        else:
            result[module] = entry
    if not result:
        raise RuntimeError("go-licenses returned no dependencies")
    return result


def legal_files(module_dir: Path) -> list[Path]:
    """Return every top-level license/patent/attribution file, not only the root license."""
    files = [
        path for path in module_dir.iterdir()
        if path.is_file() and LEGAL_FILE.match(path.name)
    ]
    files.sort(key=lambda path: path.name)
    if not files:
        raise RuntimeError(f"no legal notice files found in {module_dir}")
    return files


def validate_assets(actual: set[str], reviewed: set[str]) -> None:
    new = sorted(actual - reviewed)
    missing = sorted(reviewed - actual)
    if new:
        raise RuntimeError("unreviewed assets require license review: " + ", ".join(new))
    if missing:
        raise RuntimeError(
            "missing reviewed assets; refresh the first-party inventory: " + ", ".join(missing)
        )


def run(command: list[str], cwd: Path = ROOT, env: dict[str, str] | None = None) -> str:
    completed = subprocess.run(
        command,
        cwd=cwd,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=None,
        env=env,
    )
    return completed.stdout


def license_tool() -> str:
    configured = os.environ.get("GO_LICENSES", "go-licenses")
    resolved = shutil.which(configured)
    if not resolved:
        raise RuntimeError(
            "go-licenses v1.6.0 is required; install it with "
            "`go install github.com/google/go-licenses@v1.6.0`"
        )
    metadata = run(["go", "version", "-m", resolved])
    if not re.search(r"(?m)^\s*mod\s+github\.com/google/go-licenses\s+v1\.6\.0(?:\s|$)", metadata):
        raise RuntimeError(f"go-licenses binary is not v1.6.0: {resolved}")
    return resolved


def module_versions(cwd: Path, env: dict[str, str]) -> dict[str, str]:
    versions: dict[str, str] = {}
    output = run(["go", "list", "-m", "-f", "{{.Path}}|{{.Version}}", "all"], cwd, env)
    for line in output.splitlines():
        module, separator, version = line.partition("|")
        if separator and module:
            versions[module] = version
    return versions


def module_details(module: str) -> tuple[str, Path]:
    raw = run(["go", "list", "-m", "-json", module])
    details = json.loads(raw)
    version = details.get("Version", "")
    directory = details.get("Dir", "")
    if not version or not directory:
        raise RuntimeError(f"module has no cached source directory: {module}")
    return version, Path(directory)


def inventory(
    package: str,
    ignored_prefix: str,
    cwd: Path,
    goarch: str,
    *,
    include_tests: bool = False,
) -> tuple[str, dict[str, ModuleLicense]]:
    env = os.environ.copy()
    env.update({"GOOS": "linux", "GOARCH": goarch, "CGO_ENABLED": "0", "GOPROXY": "off"})
    command = [license_tool(), "report", package, "--ignore", ignored_prefix]
    if include_tests:
        command.append("--include_tests")
    raw = run(command, cwd, env)
    return raw, parse_report(raw, module_versions(cwd, env))


def reviewed_assets() -> set[str]:
    if not REVIEWED_ASSETS.is_file():
        raise RuntimeError("licenses/first-party-assets.txt is missing")
    return {
        line.strip() for line in REVIEWED_ASSETS.read_text(encoding="utf-8").splitlines()
        if line.strip() and not line.startswith("#")
    }


def tracked_assets() -> set[str]:
    tracked = run(["git", "ls-files"]).splitlines()
    return {path for path in tracked if Path(path).suffix.lower() in ASSET_SUFFIXES}


def verify_project_license() -> None:
    license_path = ROOT / "LICENSE"
    if not license_path.is_file() or "GNU AFFERO GENERAL PUBLIC LICENSE" not in license_path.read_text(
        encoding="utf-8"
    ):
        raise RuntimeError("LICENSE must contain the AGPLv3 license text")


def merge_platform_entries(
    target: dict[str, ModuleLicense],
    source: dict[str, ModuleLicense],
) -> None:
    for module, entry in source.items():
        previous = target.get(module)
        if previous and previous != entry:
            raise RuntimeError(f"platform reports disagree for {module}")
        target[module] = entry


def generate() -> list[dict]:
    verify_project_license()
    validate_assets(tracked_assets(), reviewed_assets())
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)

    runtime_entries: dict[str, ModuleLicense] = {}
    module_platforms: dict[str, set[str]] = {}
    for goarch in ("amd64", "arm64"):
        raw, entries = inventory("./cmd/repairflow", "repairflow", ROOT, goarch)
        (OUTPUT_DIR / f"runtime-linux-{goarch}-report.tsv").write_text(raw, encoding="utf-8")
        merge_platform_entries(runtime_entries, entries)
        for module in entries:
            module_platforms.setdefault(module, set()).add(f"linux/{goarch}")

    # Development-only inventory. Do not include these notices in the binary bundle.
    e2e_raw, _ = inventory(
        "./...", "repairflow-e2e", ROOT / "tests" / "e2e", "amd64", include_tests=True
    )
    (OUTPUT_DIR / "browser-tests-report.tsv").write_text(e2e_raw, encoding="utf-8")

    machine_inventory = []
    for module in sorted(runtime_entries):
        version, directory = module_details(module)
        machine_inventory.append({
            "module": module,
            "version": version,
            "license": runtime_entries[module].license,
            "source": runtime_entries[module].url,
            "platforms": sorted(module_platforms[module]),
            "notice_files": [path.name for path in legal_files(directory)],
        })
    (OUTPUT_DIR / "runtime-inventory.json").write_text(
        json.dumps(machine_inventory, indent=2) + "\n", encoding="utf-8"
    )
    return machine_inventory


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("check", "summary"))
    args = parser.parse_args()
    try:
        entries = generate()
        if args.action == "check":
            print(f"license inventory and first-party assets: PASS ({len(entries)} runtime modules)")
        else:
            print(f"runtime modules: {len(entries)}")
            for entry in entries:
                print(f"{entry['module']} {entry['version']}: {entry['license']}")
        return 0
    except (RuntimeError, subprocess.CalledProcessError, OSError, ValueError) as exc:
        print(f"license check: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
