// Package update replaces the running memorable binary with a newer, GPG-verified GitHub release.
package update

import (
	"errors"
	"fmt"
)

// ErrUnsupportedPlatform means no release archive exists for this OS and architecture.
var ErrUnsupportedPlatform = errors.New("no release archive for this platform")

// AssetName returns the release archive for goos and goarch. Names follow `uname -s` and `uname -m`,
// matching the GoReleaser name_template.
func AssetName(goos, goarch string) (string, error) {
	osName := map[string]string{"linux": "Linux", "darwin": "Darwin"}[goos]
	arch := map[string]map[string]string{
		"linux":  {"amd64": "x86_64", "arm64": "aarch64"},
		"darwin": {"amd64": "x86_64", "arm64": "arm64"},
	}[goos][goarch]
	if osName == "" || arch == "" {
		return "", fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, goos, goarch)
	}
	return "memorable_" + osName + "_" + arch + ".tar.gz", nil
}
