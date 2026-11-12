// Package lazareplay holds the version number shared by the app and the
// self-test.
package lazareplay

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var version string

// Version returns the release number from the VERSION file.
func Version() string { return strings.TrimSpace(version) }
