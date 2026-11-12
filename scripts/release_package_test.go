package scripts

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestPackageReleaseWritesOnlyInstallableArchives(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	amd64 := write("candidate-amd64", "amd64 binary fixture\n")
	arm64 := write("candidate-arm64", "arm64 binary fixture\n")
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "package-release.sh",
		"--out", out,
		"--amd64", amd64,
		"--arm64", arm64,
		"--license", write("LICENSE", "license fixture\n"),
		"--notices", write("THIRD_PARTY_NOTICES.md", "notices fixture\n"),
		"--manifest", write("manifest.json", `{"format_version":1}`+"\n"),
		"--signature", write("manifest.sig", "signature fixture\n"),
		"--update-script", write("update-github.sh", "#!/bin/sh\n"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("package release: %v\n%s", err, output)
	}

	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	wantNames := []string{
		"SHA256SUMS",
		"repairflow_Linux_arm64.tar.gz",
		"repairflow_Linux_x86_64.tar.gz",
	}
	if strings.Join(names, "\n") != strings.Join(wantNames, "\n") {
		t.Fatalf("release assets = %v, want %v", names, wantNames)
	}

	wantMembers := []string{
		"repairflow",
		"LICENSE",
		"THIRD_PARTY_NOTICES.md",
		"manifest.json",
		"manifest.sig",
		"update-github.sh",
	}
	for _, archive := range wantNames[1:] {
		path := filepath.Join(out, archive)
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		var members []string
		contents := map[string]string{}
		modes := map[string]int64{}
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if hdr.Typeflag != tar.TypeReg {
				t.Fatalf("%s contains non-regular member %q (type %d)", archive, hdr.Name, hdr.Typeflag)
			}
			members = append(members, hdr.Name)
			modes[hdr.Name] = hdr.Mode
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			contents[hdr.Name] = string(body)
		}
		_ = gz.Close()
		_ = f.Close()
		if strings.Join(members, "\n") != strings.Join(wantMembers, "\n") {
			t.Fatalf("%s members = %v, want %v", archive, members, wantMembers)
		}
		wantBinary := "amd64 binary fixture\n"
		if strings.Contains(archive, "arm64") {
			wantBinary = "arm64 binary fixture\n"
		}
		if contents["repairflow"] != wantBinary {
			t.Fatalf("%s binary = %q, want %q", archive, contents["repairflow"], wantBinary)
		}
		if modes["repairflow"] != 0o755 || modes["update-github.sh"] != 0o755 {
			t.Fatalf("%s executable modes = %o/%o", archive, modes["repairflow"], modes["update-github.sh"])
		}
		if modes["LICENSE"] != 0o644 || modes["manifest.json"] != 0o644 {
			t.Fatalf("%s data modes = %o/%o", archive, modes["LICENSE"], modes["manifest.json"])
		}
	}

	sums, err := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(sums)), "\n")
	if len(lines) != 2 {
		t.Fatalf("SHA256SUMS lines = %d, want 2\n%s", len(lines), sums)
	}
	for _, archive := range wantNames[1:] {
		body, err := os.ReadFile(filepath.Join(out, archive))
		if err != nil {
			t.Fatal(err)
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(body))
		if !strings.Contains(string(sums), hash+"  "+archive) {
			t.Fatalf("SHA256SUMS missing %s entry", archive)
		}
	}
}
