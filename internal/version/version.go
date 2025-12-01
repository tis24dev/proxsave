package version

import (
	"runtime/debug"
	"strings"
)

// These variables are intended to be populated at build time via -ldflags.
// For example, GoReleaser injects:
//   -X github.com/tis24dev/proxsave/internal/version.Version=v0.9.0
//   -X github.com/tis24dev/proxsave/internal/version.Commit=abcdef123
//   -X github.com/tis24dev/proxsave/internal/version.Date=2025-01-01T12:34:56Z
var (
	// Version holds the semantic version of the binary.
	// Defaults to a development placeholder when not set by the build system.
	Version = "0.0.0-dev"

	// Commit holds the VCS commit hash used to build the binary (optional).
	Commit = ""

	// Date holds the build timestamp (optional).
	Date = ""
)

// String returns the effective version string used across the application.
// Preference order:
//   1. Value injected into Version via ldflags (e.g., GoReleaser).
//   2. Main module version from debug.ReadBuildInfo (if available and not "(devel)").
//   3. Fallback development placeholder.
//
// The returned version is normalized by stripping any leading "v" prefix.
func String() string {
	v := strings.TrimSpace(Version)

	if v == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			if mv := strings.TrimSpace(info.Main.Version); mv != "" && mv != "(devel)" {
				v = mv
			}
		}
	}

	if v == "" {
		v = "0.0.0-dev"
	}

	// Normalize common "vX.Y.Z" tag format.
	v = strings.TrimPrefix(v, "v")

	return v
}

