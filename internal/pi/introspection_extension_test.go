package pi

import (
	"strings"
	"testing"
)

func TestRadarExtensionRegistersWorkspaceIntrospectionTools(t *testing.T) {
	text := string(radarExtension)
	for _, required := range []string{
		"radar_workspace_context",
		"radar_repository_refs",
		`["workspace-context", "--workspace"`,
		`["repository-refs", "--repo"`,
		"NoParameters",
		"RepositoryParameters",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("extension is missing %q", required)
		}
	}
}
