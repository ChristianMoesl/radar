// Package hosttemp keeps Radar's host runtime paths stable when Pi redirects
// its own TMPDIR to a workspace's shared directory.
package hosttemp

import "os"

const EnvironmentVariable = "RADAR_HOST_TMPDIR"

func Dir() string {
	if root := os.Getenv(EnvironmentVariable); root != "" {
		return root
	}
	return os.TempDir()
}
