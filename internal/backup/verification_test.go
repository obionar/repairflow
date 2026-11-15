package backup_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"repairflow/internal/backup"
	"repairflow/internal/config"
	"repairflow/internal/errs"
	"repairflow/internal/testutil"
)

// Rewrite a genuine archive so malformed metadata still has valid payload hashes.
func rewriteBackup(t *testing.T, source, dest string, change func(*backup.Manifest, map[string][]byte)) {
	t.Helper()
	in, err := os.Open(source)
	testutil.Must(t, err)
	defer testutil.Close(t, in)
	gz, err := gzip.NewReader(in)
	testutil.Must(t, err)
	defer testutil.Close(t, gz)
	tr := tar.NewReader(gz)
	var manifest backup.Manifest
	bodies := map[string][]byte{}
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		testutil.Must(t, err)
		body, err := io.ReadAll(tr)
		testutil.Must(t, err)
		if hdr.Name == "manifest.json" {
			testutil.Must(t, json.Unmarshal(body, &manifest))
		} else {
			bodies[hdr.Name] = body
			names = append(names, hdr.Name)
		}
	}
	change(&manifest, bodies)
	body, err := json.Marshal(manifest)
	testutil.Must(t, err)
	bodies["manifest.json"] = body
	out, err := os.Create(dest)
	testutil.Must(t, err)
	defer testutil.Close(t, out)
	writer := gzip.NewWriter(out)
	defer testutil.Close(t, writer)
	tw := tar.NewWriter(writer)
	defer testutil.Close(t, tw)
	for _, name := range append([]string{"manifest.json"}, names...) {
		body := bodies[name]
		testutil.Must(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}))
		_, err := tw.Write(body)
		testutil.Must(t, err)
	}
}

func TestVerifyAndRestoreRejectInconsistentMetadata(t *testing.T) {
	src := newWorkspace(t)
	defer testutil.Close(t, src.db)
	src.populated(t)
	testutil.Must(t, os.WriteFile(filepath.Join(src.home, config.ConfigName), config.Render(config.Default()), 0o600))
	good := filepath.Join(t.TempDir(), "good.rfbak")
	_, err := backup.Create(context.Background(), src.home, good, "tester")
	testutil.Must(t, err)

	for name, change := range map[string]func(*backup.Manifest, map[string][]byte){
		"database size":   func(m *backup.Manifest, _ map[string][]byte) { m.Database.SizeBytes++ },
		"config size":     func(m *backup.Manifest, _ map[string][]byte) { m.Config.SizeBytes++ },
		"attachment size": func(m *backup.Manifest, _ map[string][]byte) { m.Attachments[0].SizeBytes++ },
		"negative size":   func(m *backup.Manifest, _ map[string][]byte) { m.Database.SizeBytes = -1 },
		"database attachment size disagreement": func(m *backup.Manifest, bodies map[string][]byte) {
			a := &m.Attachments[0]
			for name, body := range bodies {
				if filepath.Base(name) == a.StorageName {
					body = append(body, 0)
					bodies[name] = body
					sum := sha256.Sum256(body)
					a.SHA256 = hex.EncodeToString(sum[:])
					a.SizeBytes++
				}
			}
		},
		"schema mismatch": func(m *backup.Manifest, _ map[string][]byte) { m.SchemaVersion-- },
		"duplicate attachment": func(m *backup.Manifest, _ map[string][]byte) {
			m.Attachments = append(m.Attachments, m.Attachments[0])
		},
		"invalid config": func(m *backup.Manifest, bodies map[string][]byte) {
			body := []byte("config_version = 1\n[web]\nport = 0\n")
			bodies[config.ConfigName] = body
			sum := sha256.Sum256(body)
			m.Config.SizeBytes = int64(len(body))
			m.Config.SHA256 = hex.EncodeToString(sum[:])
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := filepath.Join(t.TempDir(), "bad.rfbak")
			rewriteBackup(t, good, bad, change)
			if _, err := backup.Verify(context.Background(), bad); errs.CodeOf(err) != errs.CodeValidation {
				t.Fatalf("verify must reject with validation_error, got %v", err)
			}
			dest := filepath.Join(t.TempDir(), "restored")
			if _, err := backup.Restore(context.Background(), bad, dest); errs.CodeOf(err) != errs.CodeValidation {
				t.Fatalf("restore must reject with validation_error, got %v", err)
			}
			if entries, err := os.ReadDir(dest); err == nil && len(entries) != 0 {
				t.Fatalf("failed restore published content: %v", entries)
			}
		})
	}
}

func TestVerifyAndRestoreCheckGzipTrailer(t *testing.T) {
	src := newWorkspace(t)
	defer testutil.Close(t, src.db)
	good := filepath.Join(t.TempDir(), "good.rfbak")
	_, err := backup.Create(context.Background(), src.home, good, "tester")
	testutil.Must(t, err)
	original, err := os.ReadFile(good)
	testutil.Must(t, err)
	for _, mode := range []string{"corrupt checksum", "missing trailer"} {
		t.Run(mode, func(t *testing.T) {
			body := append([]byte(nil), original...)
			if mode == "missing trailer" {
				body = body[:len(body)-8]
			} else {
				body[len(body)-8] ^= 0xff
			}
			bad := filepath.Join(t.TempDir(), "bad.rfbak")
			testutil.Must(t, os.WriteFile(bad, body, 0o600))
			if _, err := backup.Verify(context.Background(), bad); errs.CodeOf(err) != errs.CodeValidation {
				t.Fatalf("verify accepted damaged gzip trailer: %v", err)
			}
			if _, err := backup.Restore(context.Background(), bad, filepath.Join(t.TempDir(), "restore")); errs.CodeOf(err) != errs.CodeValidation {
				t.Fatalf("restore accepted damaged gzip trailer: %v", err)
			}
		})
	}
}

func TestConfigAndPrinterSettingRoundtrip(t *testing.T) {
	src := newWorkspace(t)
	defer testutil.Close(t, src.db)
	ctx := context.Background()
	cfg := config.Default()
	cfg.UI.Language = "ru"
	cfg.Web.Port = 8123
	testutil.Must(t, os.WriteFile(filepath.Join(src.home, config.ConfigName), config.Render(cfg), 0o600))
	_, err := src.svc.SetLabelPrinter(ctx, "admin", "p15")
	testutil.Must(t, err)
	archive := filepath.Join(t.TempDir(), "printer.rfbak")
	_, err = backup.Create(ctx, src.home, archive, "tester")
	testutil.Must(t, err)
	dst := restoredWorkspace(t, archive)
	got, err := config.Load(filepath.Join(dst.home, config.ConfigName))
	testutil.Must(t, err)
	if got != cfg {
		t.Fatalf("restored config = %+v, want %+v", got, cfg)
	}
	printer, err := dst.svc.LabelPrinter(ctx)
	testutil.Must(t, err)
	if printer.Driver != "p15" {
		t.Fatalf("restored label printer = %+v", printer)
	}
}
