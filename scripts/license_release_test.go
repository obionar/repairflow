package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseGateIncludesLicenseInventoryAndNotices(t *testing.T) {
	for name, want := range map[string][]string{
		"release.sh": {
			"license_inventory.py check",
			`install -m 0644 LICENSE "$OUT/LICENSE"`,
			`install -m 0644 THIRD_PARTY_NOTICES.md "$OUT/THIRD_PARTY_NOTICES.md"`,
			`install -m 0755 scripts/update-github.sh "$OUT/update-github.sh"`,
			`repairflow-source-$COMMIT.tar.gz`,
			`LICENSE THIRD_PARTY_NOTICES.md repairflow-source-$COMMIT.tar.gz run-demo.sh update-github.sh`,
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
