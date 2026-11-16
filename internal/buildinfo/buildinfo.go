// Package buildinfo carries build-time metadata injected at link time.
//
// The values are overridden with -ldflags "-X repairflow/internal/buildinfo.Version=...".
// Unmodified development builds report the defaults below; they are never
// presented as release versions.
package buildinfo

import "runtime"

// Build metadata. Defaults describe an un-stamped development build.
var (
	Version = "0.2.0-dev"
	Commit  = "unknown"
	Date    = "unknown"
	// PublicKey is the hex Ed25519 release-signing key embedded at build
	// time (-X repairflow/internal/buildinfo.PublicKey=...). Empty means
	// this build refuses to apply updates.
	PublicKey = ""
	// SourceURL is where the Corresponding Source of this exact build is
	// published. A modified deployment points it at its own source with
	// -X repairflow/internal/buildinfo.SourceURL=..., as AGPL-3.0 section 13
	// requires of distributors.
	SourceURL = "https://github.com/obionar/repairflow"
)

// Info is the machine-readable build description reported by `repairflow version`.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
	SourceURL string `json:"source_url"`
}

// Get returns the current build description.
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		SourceURL: SourceURL,
	}
}
