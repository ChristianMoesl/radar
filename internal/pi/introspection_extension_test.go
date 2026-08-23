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
		"radar_reconcile_workspace",
		`["workspace-context", "--workspace"`,
		`["repository-refs", "--repo"`,
		`["reconcile-workspace", "--workspace"`,
		"NoParameters",
		"RepositoryParameters",
		"MaxReconcileConfirmations",
		"reconfirm_required",
		"Workspace plan changed. Reconcile updated plan?",
		"displayChangeSummary",
		"homeRelativePath",
		"change.summary.replace(change.path, shortened)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("extension is missing %q", required)
		}
	}
}
