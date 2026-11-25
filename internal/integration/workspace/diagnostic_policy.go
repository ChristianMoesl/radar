package workspace

import (
	"fmt"
	"path/filepath"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

var _ integration.WorkspacePolicyDiagnostics = Source{}

func (Source) DiagnosticWorkspacePolicy(id, scope string) (protocol.DiagnosticWorkspacePolicy, error) {
	root, err := DefaultRoot()
	if err != nil {
		return protocol.DiagnosticWorkspacePolicy{}, fmt.Errorf("workspace recipe registration unavailable")
	}
	registry, err := workspacegroup.DiagnosticLoad(root)
	if err != nil {
		return protocol.DiagnosticWorkspacePolicy{}, fmt.Errorf("workspace recipe registration unavailable")
	}
	// DiagnosticLoad shares native identity/path and complete recipe validation.
	// Never reconstruct an origin from current files: provenance was not stored.
	for _, group := range registry.Workspaces {
		if group.ID != id || (scope != "" && filepath.Clean(scope) != filepath.Clean(group.Path)) {
			continue
		}
		view := protocol.DiagnosticWorkspacePolicy{ID: group.ID, Origin: "recorded_workspace_registration", SandboxRegistered: group.Sandbox != nil}
		if recipe := group.Sandbox; recipe != nil {
			view.Sandbox = &protocol.DiagnosticSandboxRecipe{
				KitConfigured: recipe.Agent != "", KitPathConfigured: recipe.KitPath != "",
				EnvFileConfigured: recipe.EnvFile != "", ReadyCommandConfigured: len(recipe.ReadyCommand) != 0,
				ManagedMountCount: len(recipe.Mounts), AdditionalMountCount: len(recipe.AdditionalMounts), PortCount: len(recipe.Ports),
			}
		}
		return view, nil
	}
	return protocol.DiagnosticWorkspacePolicy{}, fmt.Errorf("workspace recipe parent is not registered in the selected scope")
}
