// Package version provides build-time version information for CmdPilot.
package version

import "fmt"

// Version is the semantic version; overridable at build time via -ldflags "-X ...=x.y.z".
var Version = "0.1.0"

// Commit is the git commit hash injected at build time.
var Commit = "dev"

// BuildDate is the build timestamp injected at build time.
var BuildDate = "unknown"

// String returns a single-line human-readable version description.
func String() string {
	return fmt.Sprintf("CmdPilot %s (commit %s, built %s)", Version, Commit, BuildDate)
}
