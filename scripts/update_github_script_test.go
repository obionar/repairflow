package scripts

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func buildUpdateScriptBinary(t *testing.T, dir, version string, pub ed25519.PublicKey) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "repairflow-"+version)
	ldflags := fmt.Sprintf("-X repairflow/internal/buildinfo.Version=%s -X repairflow/internal/buildinfo.PublicKey=%s",
		version, hex.EncodeToString(pub))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", ldflags, "-o", path, "./cmd/repairflow")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, output)
	}
	return path
}

func TestGitHubUpdateScriptChecksThenAppliesSignedRelease(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("verified apply is Linux-only")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	installed := buildUpdateScriptBinary(t, dir, "0.1.0-rc9.1", pub)
	candidate := buildUpdateScriptBinary(t, dir, "0.1.0-rc9.2", pub)
	candidateBytes, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidateHash := sha256.Sum256(candidateBytes)
	tag := "0.1.0-rc9.2"
	artifactName := "repairflow-linux-" + runtime.GOARCH
	manifest, err := json.Marshal(map[string]any{
		"format_version": 1, "channel": "experimental", "version": tag,
		"created_at": "2026-11-12T00:00:00Z",
		"artifacts": []map[string]any{{
			"os": "linux", "arch": runtime.GOARCH, "file": artifactName,
			"sha256": fmt.Sprintf("%x", candidateHash), "size_bytes": len(candidateBytes),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	signature := []byte(hex.EncodeToString(ed25519.Sign(priv, manifest)) + "\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/obionar/repairflow/releases/download/" + tag + "/"
		switch strings.TrimPrefix(r.URL.Path, base) {
		case "manifest.json":
			_, _ = w.Write(manifest)
		case "manifest.sig":
			_, _ = w.Write(signature)
		case artifactName:
			_, _ = w.Write(candidateBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	run := func(apply bool) string {
		args := []string{"--tag", tag, "--binary", installed}
		if apply {
			args = append(args, "--apply")
		}
		cmd := exec.Command("sh", append([]string{"update-github.sh"}, args...)...)
		cmd.Env = append(cleanEnvironment(), "REPAIRFLOW_UPDATE_BASE_URL="+server.URL, "TMPDIR="+t.TempDir())
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("update script (apply=%t): %v\n%s", apply, err, output)
		}
		return string(output)
	}

	output := run(false)
	if !strings.Contains(output, "update verified: "+tag) {
		t.Fatalf("check output: %s", output)
	}
	assertBinaryVersion(t, installed, "0.1.0-rc9.1")
	if _, err := os.Stat(installed + ".prev"); !os.IsNotExist(err) {
		t.Fatalf("check created a rollback binary: %v", err)
	}
	if _, err := os.Stat(installed + ".new"); !os.IsNotExist(err) {
		t.Fatalf("check created a staging binary: %v", err)
	}

	output = run(true)
	if !strings.Contains(output, "updated 0.1.0-rc9.1 -> 0.1.0-rc9.2") {
		t.Fatalf("apply output: %s", output)
	}
	assertBinaryVersion(t, installed, "0.1.0-rc9.2")
	assertBinaryVersion(t, installed+".prev", "0.1.0-rc9.1")
}

func TestGitHubUpdateScriptRefusalsPreserveInstalledBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("verified apply is Linux-only")
	}
	for _, mode := range []string{"bad-signature", "hash-mismatch", "missing-artifact", "interrupted-artifact"} {
		t.Run(mode, func(t *testing.T) {
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			installed := buildUpdateScriptBinary(t, dir, "0.1.0-rc9.1", pub)
			artifact := []byte("candidate fixture\n")
			if mode == "hash-mismatch" {
				artifact = []byte("tampered candidate\n")
			}
			tag := "0.1.0-rc9.2"
			artifactName := "repairflow-linux-" + runtime.GOARCH
			hash := sha256.Sum256([]byte("candidate fixture\n"))
			manifest, err := json.MarshalIndent(map[string]any{
				"format_version": 1, "channel": "experimental", "version": tag,
				"created_at": "2026-11-12T00:00:00Z",
				"artifacts": []map[string]any{{
					"os": "linux", "arch": runtime.GOARCH, "file": artifactName,
					"sha256": fmt.Sprintf("%x", hash), "size_bytes": len([]byte("candidate fixture\n")),
				}},
			}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			signature := []byte(hex.EncodeToString(ed25519.Sign(priv, manifest)) + "\n")
			if mode == "bad-signature" {
				signature = []byte("00\n")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				base := "/obionar/repairflow/releases/download/" + tag + "/"
				switch strings.TrimPrefix(r.URL.Path, base) {
				case "manifest.json":
					_, _ = w.Write(manifest)
				case "manifest.sig":
					_, _ = w.Write(signature)
				case artifactName:
					if mode == "missing-artifact" {
						http.NotFound(w, r)
						return
					}
					if mode == "interrupted-artifact" {
						w.Header().Set("Content-Length", fmt.Sprint(len(artifact)+10))
					}
					_, _ = w.Write(artifact)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			tempRoot := t.TempDir()
			cmd := exec.Command("sh", "update-github.sh", "--tag", tag, "--binary", installed, "--apply")
			cmd.Env = append(cleanEnvironment(), "REPAIRFLOW_UPDATE_BASE_URL="+server.URL, "TMPDIR="+tempRoot)
			if output, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("refused update succeeded: %s", output)
			}
			assertBinaryVersion(t, installed, "0.1.0-rc9.1")
			if _, err := os.Stat(installed + ".prev"); !os.IsNotExist(err) {
				t.Fatalf("refused update created rollback file: %v", err)
			}
			entries, err := os.ReadDir(tempRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary downloads were not cleaned: %v %v", entries, err)
			}
		})
	}
}

func TestGitHubUpdateScriptApplyRefusalPreservesBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("verified apply is Linux-only")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	installed := buildUpdateScriptBinary(t, dir, "0.1.0-rc9.1", pub)
	installedBytes, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	tag := "0.1.0-rc9.2"
	artifactName := "repairflow-linux-" + runtime.GOARCH
	hash := sha256.Sum256(installedBytes)
	manifest, err := json.Marshal(map[string]any{
		"format_version": 1, "channel": "experimental", "version": "0.1.0-rc9.1",
		"created_at": "2026-11-12T00:00:00Z",
		"artifacts": []map[string]any{{
			"os": "linux", "arch": runtime.GOARCH, "file": artifactName,
			"sha256": fmt.Sprintf("%x", hash), "size_bytes": len(installedBytes),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	signature := []byte(hex.EncodeToString(ed25519.Sign(priv, manifest)) + "\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/obionar/repairflow/releases/download/" + tag + "/"
		switch strings.TrimPrefix(r.URL.Path, base) {
		case "manifest.json":
			_, _ = w.Write(manifest)
		case "manifest.sig":
			_, _ = w.Write(signature)
		case artifactName:
			_, _ = w.Write(installedBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cmd := exec.Command("sh", "update-github.sh", "--tag", tag, "--binary", installed, "--apply")
	cmd.Env = append(cleanEnvironment(), "REPAIRFLOW_UPDATE_BASE_URL="+server.URL, "TMPDIR="+t.TempDir())
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "already installed") {
		t.Fatalf("same-version apply must be refused: %v\n%s", err, output)
	}
	assertBinaryVersion(t, installed, "0.1.0-rc9.1")
	for _, suffix := range []string{".prev", ".new"} {
		if _, err := os.Stat(installed + suffix); !os.IsNotExist(err) {
			t.Fatalf("refused apply created %s: %v", suffix, err)
		}
	}
}

func cleanEnvironment() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "REPAIRFLOW_UPDATE_BASE_URL=") || strings.HasPrefix(entry, "TMPDIR=") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func assertBinaryVersion(t *testing.T, path, want string) {
	t.Helper()
	output, err := exec.Command(path, "version", "--json").CombinedOutput()
	if err != nil || !strings.Contains(string(output), `"version":"`+want+`"`) {
		t.Fatalf("%s version: %v\n%s", path, err, output)
	}
}
