// Package version holds the application version, read from the VERSION file
// next to this source so releases can check the tag against one place.
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// override replaces VERSION at build time (-X), for CI tests of updates.
var override string

// Version is the running version, for example "2.0.0" or "2.1.0-dev".
var Version = pick()

func pick() string {
	if override != "" {
		return override
	}
	return strings.TrimSpace(raw)
}

const (
	// Name is the full product name.
	Name = "PBC Manager"
	// ShortName is used where space is tight: the sidebar, authenticator apps.
	ShortName = "PBC Manager"
)
