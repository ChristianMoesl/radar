package workspace

import "testing"

// UseFixturePlatform shares the existing fake-provider platform seam with the
// external integration tests. It is not compiled into Radar or its public API.
// No test using this process-local seam may run in parallel.
func UseFixturePlatform(t *testing.T, goos string) {
	withWorkspaceGOOS(t, goos)
}
