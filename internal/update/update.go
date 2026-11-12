// Package update implements verified local-file updates with signed manifests.
//
// Trust model: a release ships `manifest.json` + `manifest.sig` (Ed25519
// detached signature over the exact manifest bytes) + the artifact. The
// trusted public key is embedded in the binary at build time
// (buildinfo.PublicKey, set via -ldflags). SHA-256 verification of the
// artifact is mandatory on top of the signature.
//
// v0.1 updates are OFFLINE: `update check/apply` read local files only. No
// network fetching, no script execution. The update helper is compiled into
// the same single binary.
package update

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"repairflow/internal/errs"
)

// FormatVersion is the release manifest format version.
const FormatVersion = 1

// Artifact describes one platform binary in a release.
type Artifact struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	File      string `json:"file"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// Manifest is the signed release record.
type Manifest struct {
	FormatVersion int        `json:"format_version"`
	Channel       string     `json:"channel"`
	Version       string     `json:"version"`
	CreatedAt     string     `json:"created_at"`
	Artifacts     []Artifact `json:"artifacts"`
}

// CheckResult reports a verified update candidate.
type CheckResult struct {
	CurrentVersion string   `json:"current_version"`
	NewVersion     string   `json:"new_version"`
	Channel        string   `json:"channel"`
	Artifact       Artifact `json:"artifact"`
	UpToDate       bool     `json:"up_to_date"`
}

// ParsePublicKey decodes a hex-encoded Ed25519 public key.
func ParsePublicKey(hexKey string) (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errs.New(errs.CodeValidation,
			"release public key must be %d hex characters (ed25519)", 2*ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// VerifyManifest checks the detached Ed25519 signature over the manifest
// bytes and validates the manifest's internal consistency.
func VerifyManifest(manifestBytes, sig []byte, pub ed25519.PublicKey) (*Manifest, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, errs.New(errs.CodeValidation, "no trusted release key is embedded in this build")
	}
	sigBytes, err := hex.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return nil, errs.New(errs.CodeValidation, "signature must be hex-encoded")
	}
	if !ed25519.Verify(pub, manifestBytes, sigBytes) {
		return nil, errs.New(errs.CodeValidation, "release manifest signature is invalid")
	}
	var m Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return nil, errs.Wrap(errs.CodeValidation, err, "release manifest is corrupt")
	}
	if m.FormatVersion != FormatVersion {
		return nil, errs.New(errs.CodeConfigVersion, "manifest format %d is not supported", m.FormatVersion)
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, errs.New(errs.CodeValidation, "manifest has no version")
	}
	for _, a := range m.Artifacts {
		if a.OS == "" || a.Arch == "" || a.File == "" || len(a.SHA256) != 64 || a.SizeBytes <= 0 {
			return nil, errs.New(errs.CodeValidation, "manifest artifact entry is incomplete")
		}
	}
	return &m, nil
}

// Check verifies a local release against the running version (read-only).
func Check(manifestBytes, sig []byte, pub ed25519.PublicKey, currentVersion, goos, goarch string) (*CheckResult, error) {
	m, err := VerifyManifest(manifestBytes, sig, pub)
	if err != nil {
		return nil, err
	}
	artifact, err := m.matchArtifact(goos, goarch)
	if err != nil {
		return nil, err
	}
	res := &CheckResult{
		CurrentVersion: currentVersion, NewVersion: m.Version,
		Channel: m.Channel, Artifact: *artifact,
	}
	res.UpToDate = CompareVersions(m.Version, currentVersion) == 0
	return res, nil
}

// matchArtifact finds the artifact for this platform.
func (m *Manifest) matchArtifact(goos, goarch string) (*Artifact, error) {
	for i := range m.Artifacts {
		if m.Artifacts[i].OS == goos && m.Artifacts[i].Arch == goarch {
			return &m.Artifacts[i], nil
		}
	}
	return nil, errs.New(errs.CodeValidation,
		"release has no artifact for %s/%s (this build cannot update to it)", goos, goarch)
}

// CompareVersions compares dotted numeric versions (-1, 0, 1). Non-numeric
// segments fall back to lexicographic comparison of that segment.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		xi, xe := strconv.Atoi(x)
		yi, ye := strconv.Atoi(y)
		switch {
		case xe == nil && ye == nil:
			if xi != yi {
				if xi < yi {
					return -1
				}
				return 1
			}
		default:
			if x != y {
				if x < y {
					return -1
				}
				return 1
			}
		}
	}
	return 0
}

// HashFile computes a regular file's SHA-256 and size without following a
// symlink or reading a FIFO/device. It shares the updater's one-stream
// inspection rules with Apply.
func HashFile(path string) (string, int64, error) {
	return hashArtifactFile(path)
}
