package pi

import (
	"strings"
	"testing"
)

func TestRadarExtensionRegistersWorkspaceIntrospectionTools(t *testing.T) {
	text := string(radarExtension)
	if strings.Contains(text, "minItems: 1") {
		t.Fatal("workspace reconciliation schema still requires one worktree")
	}
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
		`pi.on("resources_discover"`,
		`pi.on("before_agent_start"`,
		"instruction_files",
		"skill_paths",
		"radar-reload-workspace-resources",
		"Member worktrees are direct children",
		"duplicate skill",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("extension is missing %q", required)
		}
	}
}
