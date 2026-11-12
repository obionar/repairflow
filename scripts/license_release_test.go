package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseScriptShipsOnlyPlatformArchives(t *testing.T) {
	body, err := os.ReadFile("release.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"scripts/package-release.sh",
		`--amd64 "$BUILD/repairflow-amd64"`,
		`--arm64 "$BUILD/repairflow-arm64"`,
		`--out "$OUT"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("release.sh does not contain %q", want)
		}
	}
	for _, forbidden := range []string{
		`repairflow-source-$COMMIT.tar.gz`,
		`install -m 0644 README.md "$OUT/README.md"`,
		`install -m 0755 scripts/run-demo.sh "$OUT/run-demo.sh"`,
		`install -m 0755 scripts/update-github.sh "$OUT/update-github.sh"`,
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("release.sh still writes loose release asset %q", forbidden)
		}
	}
}

func TestReleaseGateIncludesLicenseInventoryAndNotices(t *testing.T) {
	for name, want := range map[string][]string{
		"release.sh": {
			"license_inventory.py check",
			"scripts/package-release.sh",
			"--license LICENSE",
			"--notices THIRD_PARTY_NOTICES.md",
		},
		"verify-all.sh": {
			"step_license_inventory",
			`run_step "license inventory" step_license_inventory`,
		},
		"../Makefile": {
			"license-check:",
			"licenses:",
		},
	} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, needle := range want {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s does not contain required license gate text %q", name, needle)
			}
		}
	}
}
