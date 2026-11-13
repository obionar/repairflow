#!/usr/bin/env python3
"""Unit tests for scripts/license_inventory.py (standard library only)."""
from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
import unittest

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "license_inventory", ROOT / "scripts" / "license_inventory.py"
)
assert SPEC is not None and SPEC.loader is not None
license_inventory = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = license_inventory
SPEC.loader.exec_module(license_inventory)

VERSIONS = {
    "modernc.org/libc": "v1.80.1",
    "modernc.org/mathutil": "v1.7.1",
    "modernc.org/memory": "v1.12.1",
    "github.com/google/uuid": "v1.6.0",
    "golang.org/x/sys": "v0.50.0",
}


class LicenseInventoryTests(unittest.TestCase):
    def test_report_requires_known_licenses_and_applies_reviewed_overrides(self):
        rows = license_inventory.parse_report(
            "modernc.org/mathutil,Unknown,Unknown\n"
            "modernc.org/libc,https://gitlab.com/cznic/libc/blob/v1.80.1/LICENSE-3RD-PARTY.md,MIT\n"
            "modernc.org/memory,https://gitlab.com/cznic/memory/blob/v1.12.1/LICENSE-GO,BSD-3-Clause\n"
            "github.com/google/uuid,https://example.invalid/LICENSE,BSD-3-Clause\n",
            VERSIONS,
        )
        self.assertEqual(rows["modernc.org/mathutil"].license, "BSD-3-Clause")
        self.assertEqual(rows["modernc.org/libc"].license, "BSD-3-Clause")
        self.assertEqual(
            rows["modernc.org/libc"].url,
            "https://gitlab.com/cznic/libc/blob/v1.80.1/LICENSE",
        )
        self.assertEqual(
            rows["modernc.org/memory"].url,
            "https://gitlab.com/cznic/memory/blob/v1.12.1/LICENSE",
        )
        with self.assertRaisesRegex(RuntimeError, "unknown module"):
            license_inventory.parse_report("example.com/unknown,Unknown,Unknown\n")

    def test_override_refuses_new_version_or_changed_detection(self):
        raw = "modernc.org/libc,https://gitlab.com/cznic/libc/blob/v1.80.1/LICENSE-3RD-PARTY.md,MIT\n"
        changed_version = dict(VERSIONS, **{"modernc.org/libc": "v9.0.0"})
        with self.assertRaisesRegex(RuntimeError, "override mismatch"):
            license_inventory.parse_report(raw, changed_version)
        changed_license = raw.replace(",MIT", ",BSD-3-Clause")
        with self.assertRaisesRegex(RuntimeError, "override mismatch"):
            license_inventory.parse_report(changed_license, VERSIONS)

    def test_report_maps_package_paths_to_modules_and_merges(self):
        rows = license_inventory.parse_report(
            "golang.org/x/sys/unix,https://example.invalid/LICENSE,BSD-3-Clause\n"
            "golang.org/x/sys/cpu,https://example.invalid/LICENSE,BSD-3-Clause\n",
            VERSIONS,
        )
        self.assertEqual(list(rows), ["golang.org/x/sys"])
        with self.assertRaisesRegex(RuntimeError, "conflicting licenses"):
            license_inventory.parse_report(
                "golang.org/x/sys/unix,https://example.invalid/LICENSE,BSD-3-Clause\n"
                "golang.org/x/sys/cpu,https://example.invalid/LICENSE,MIT\n",
                VERSIONS,
            )

    def test_asset_review_rejects_new_and_missing_entries(self):
        reviewed = {"internal/web/static/favicon.svg", "internal/web/static/app.css"}
        license_inventory.validate_assets(reviewed, reviewed)
        with self.assertRaisesRegex(RuntimeError, "unreviewed assets"):
            license_inventory.validate_assets(
                reviewed | {"assets/logo.png"}, reviewed
            )
        with self.assertRaisesRegex(RuntimeError, "missing reviewed assets"):
            license_inventory.validate_assets(set(), reviewed)


if __name__ == "__main__":
    unittest.main()
