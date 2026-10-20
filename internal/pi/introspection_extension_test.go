package pi

import (
	"strings"
	"testing"
)

func TestRadarExtensionRegistersWorkspaceIntrospectionTools(t *testing.T) {
	text := extensionSource(t)
	if strings.Contains(text, "minItems: 1") {
		t.Fatal("workspace reconciliation schema still requires one worktree")
	}
	for _, forbidden := range []string{
		"asking for confirmation unless workspace.auto_confirm is enabled",
		"with configured confirmation behavior",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("extension still tells the agent about an explicit confirmation step: %q", forbidden)
		}
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
		"auto_confirm",
		"const autoConfirm = plan.auto_confirm === true",
		"Do not ask for user confirmation before calling radar_reconcile_workspace",
		"if (!autoConfirm && !ctx.hasUI)",
		"if (!autoConfirm)",
		"reconfirm_required",
		"Workspace plan changed. Reconcile updated plan?",
		"displayChangeSummary",
		"homeRelativePath",
		"change.summary.replace(change.path, shortened)",
		`pi.on("resources_discover"`,
		`pi.on("before_agent_start"`,
		"radarInstructionsPath",
		"loadRadarInstructions",
		"XDG_CONFIG_HOME",
		"radar_instructions",
		"Instructions from Radar's user AGENTS.md apply to workspace and resource management",
		"instruction_files",
		"skill_paths",
		"radar-reload-workspace-resources",
		"Member worktrees are direct children",
		"notes.md contains user and agent content",
		"Radar task identity, title, lifecycle, priority and preferences live in SQLite",
		"preserve user content and user-owned frontmatter",
		"Do not add radar-* metadata",
		"Change task state through Radar commands",
		"Missing frontmatter is normal",

		"duplicate skill",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("extension is missing %q", required)
		}
	}
}
