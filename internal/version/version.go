// Package version contains build metadata embedded in the gg binary.
package version

import "fmt"

// These variables are intentionally package variables so release builds can
// replace them with go build -ldflags -X values. Development builds use the
// documented fallbacks below.
var (
	// Version is the semantic release version, or "dev" for local builds.
	Version = "dev"
	// Commit is the source revision, or "unknown" when not supplied.
	Commit = "unknown"
	// Date is the UTC build timestamp, or "unknown" when not supplied.
	Date = "unknown"
)

// Metadata is the immutable build metadata presented to users.
type Metadata struct {
	Version string
	Commit  string
	Date    string
}

// Current returns metadata from the linker-overridable package variables.
func Current() Metadata {
	return Metadata{Version: Version, Commit: Commit, Date: Date}
}

// String formats metadata deterministically for the CLI.
func (m Metadata) String() string {
	return fmt.Sprintf("gg version %s (commit %s, build date %s)", m.Version, m.Commit, m.Date)
}
