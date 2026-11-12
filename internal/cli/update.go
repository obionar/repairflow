package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"repairflow/internal/buildinfo"
	"repairflow/internal/errs"
	"repairflow/internal/update"
)

// Update commands: offline, verified,
// local-file only. No network access and no script execution anywhere.
func runUpdate(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow update <check|apply|keygen|sign> ...")
	}
	sub, rest := args[0], args[1:]

	switch sub {
	case "check":
		p, err := parseArgs(rest, []string{"manifest", "signature"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow update check --manifest M --signature S"); err != nil {
			return nil, err
		}
		pub, err := releaseKey()
		if err != nil {
			return nil, err
		}
		manifestBytes, sig, err := readReleaseFiles(p.get("manifest"), p.get("signature"))
		if err != nil {
			return nil, err
		}
		return update.Check(manifestBytes, sig, pub, buildinfo.Get().Version, runtime.GOOS, runtime.GOARCH)

	case "apply":
		p, err := parseArgs(rest, []string{"file", "manifest", "signature", "actor"}, []string{"confirm"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow update apply --file ARTIFACT --manifest M --signature S --confirm"); err != nil {
			return nil, err
		}
		if !p.flag("confirm") {
			return nil, errs.New(errs.CodeValidation,
				"update apply replaces the installed binary; re-run with --confirm after running 'update check'")
		}
		pub, err := releaseKey()
		if err != nil {
			return nil, err
		}
		manifestBytes, sig, err := readReleaseFiles(p.get("manifest"), p.get("signature"))
		if err != nil {
			return nil, err
		}
		target, err := os.Executable()
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot determine executable path")
		}
		return update.Apply(ctx, update.ApplyOptions{
			TargetPath: target, ArtifactPath: p.get("file"),
			ManifestBytes: manifestBytes, Signature: sig, PublicKey: pub,
			CurrentVersion: buildinfo.Get().Version,
		})

	case "keygen":
		p, err := parseArgs(rest, []string{"out"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow update keygen --out DIR"); err != nil {
			return nil, err
		}
		out := p.get("out")
		if out == "" {
			return nil, errs.New(errs.CodeUsage, "update keygen requires --out DIR")
		}
		return generateSigningKeys(out)

	case "sign":
		p, err := parseArgs(rest, []string{"key", "manifest", "out"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow update sign --key KEY --manifest M --out SIG"); err != nil {
			return nil, err
		}
		return signManifest(p.get("key"), p.get("manifest"), p.get("out"))

	default:
		return nil, errs.New(errs.CodeUsage, "unknown update subcommand %q (supported: check, apply, keygen, sign)", sub)
	}
}

// releaseKey resolves the embedded trusted release key.
func releaseKey() (ed25519.PublicKey, error) {
	if buildinfo.PublicKey == "" {
		return nil, errs.New(errs.CodeValidation,
			"this build has no embedded release public key; updates are disabled (build with -X buildinfo.PublicKey=...)")
	}
	return update.ParsePublicKey(buildinfo.PublicKey)
}

func readReleaseFiles(manifest, signature string) ([]byte, []byte, error) {
	if manifest == "" || signature == "" {
		return nil, nil, errs.New(errs.CodeUsage, "--manifest and --signature are required")
	}
	m, err := os.ReadFile(manifest)
	if err != nil {
		return nil, nil, errs.Wrap(errs.CodeValidation, err, "cannot read manifest")
	}
	s, err := os.ReadFile(signature)
	if err != nil {
		return nil, nil, errs.Wrap(errs.CodeValidation, err, "cannot read signature")
	}
	return m, s, nil
}

type keygenResult struct {
	PublicKeyFile  string `json:"public_key_file"`
	PrivateKeyFile string `json:"private_key_file"`
	PublicKeyHex   string `json:"public_key_hex"`
	Warning        string `json:"warning"`
}

type signResult struct {
	SignatureFile string `json:"signature_file"`
	PublicKeyHex  string `json:"public_key_hex"`
}

// generateSigningKeys creates a release-signing keypair. The private
// key is written owner-only and must be kept OFFLINE and OFF the appliance.
func generateSigningKeys(outDir string) (*keygenResult, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot generate signing keys")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot create key directory")
	}
	pubPath := filepath.Join(outDir, "release-signing.pub")
	privPath := filepath.Join(outDir, "release-signing.key")
	if err := os.WriteFile(pubPath, []byte(hex.EncodeToString(pub)+"\n"), 0o644); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot write public key")
	}
	if err := os.WriteFile(privPath, []byte(hex.EncodeToString(priv)+"\n"), 0o600); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot write private key")
	}
	return &keygenResult{
		PublicKeyFile: pubPath, PrivateKeyFile: privPath,
		PublicKeyHex: hex.EncodeToString(pub),
		Warning:      "keep the private key offline; embed the public key at build time via -X repairflow/internal/buildinfo.PublicKey=...",
	}, nil
}

// signManifest writes a detached Ed25519 signature over the manifest bytes.
func signManifest(keyPath, manifestPath, outPath string) (*signResult, error) {
	if keyPath == "" || manifestPath == "" || outPath == "" {
		return nil, errs.New(errs.CodeUsage, "--key, --manifest and --out are required")
	}
	keyHex, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, errs.Wrap(errs.CodeValidation, err, "cannot read signing key")
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(keyHex)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errs.New(errs.CodeValidation, "signing key must be %d hex characters (ed25519 private)", 2*ed25519.PrivateKeySize)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, errs.Wrap(errs.CodeValidation, err, "cannot read manifest")
	}
	sig := ed25519.Sign(ed25519.PrivateKey(raw), manifestBytes)
	if err := os.WriteFile(outPath, []byte(hex.EncodeToString(sig)+"\n"), 0o644); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot write signature")
	}
	return &signResult{SignatureFile: outPath, PublicKeyHex: hex.EncodeToString(ed25519.PrivateKey(raw).Public().(ed25519.PublicKey))}, nil
}
