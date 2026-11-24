package diagnostics

import (
	"fmt"
	"path/filepath"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

const workspacePolicyEvidence = "fresh_registration_recorded_recipe_not_runtime"

func workspacePolicy(store *state.Store, req protocol.DiagnosticsRequest, pending bool, providers integration.Registry) (protocol.DiagnosticsPage, error) {
	if req.ParentID == "" || len(req.ParentID) > 512 || req.ID != "" || req.State != "" || req.Cursor != "" || req.Limit < 0 || req.Limit > 50 || len(req.Workspace) > 4096 || (req.Workspace != "" && !filepath.IsAbs(req.Workspace)) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("workspace policy requires one exact registered workspace parent and no row/state/cursor selectors")
	}
	provider, err := providers.WorkspaceDiagnostics()
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("workspace recipe registration capability unavailable")
	}
	reader, ok := provider.(integration.WorkspacePolicyDiagnostics)
	if !ok {
		return protocol.DiagnosticsPage{}, fmt.Errorf("workspace recipe diagnostics unsupported")
	}
	view, err := reader.DiagnosticWorkspacePolicy(req.ParentID, req.Workspace)
	if err != nil {
		// Raw provider/parser errors can contain private paths and recipe values.
		return protocol.DiagnosticsPage{}, fmt.Errorf("workspace recipe registration unavailable in selected scope")
	}
	if view.ID != req.ParentID || view.Origin != "recorded_workspace_registration" || view.SandboxRegistered != (view.Sandbox != nil) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("workspace recipe evidence invalid")
	}
	if recipe := view.Sandbox; recipe != nil {
		// Detach the provider's public shape before validating and snapshotting
		// it. Later provider writes must not change an already-published page.
		copy := *recipe
		view.Sandbox = &copy
		for _, count := range []int{copy.ManagedMountCount, copy.AdditionalMountCount, copy.PortCount} {
			// Match the existing finite Pi contract, not effective native policy.
			if count < 0 || int64(count) > 1<<53-1 {
				return protocol.DiagnosticsPage{}, fmt.Errorf("workspace recipe evidence invalid")
			}
		}
	}
	req.Limit = 1
	return paginate(req, []protocol.DiagnosticItem{{ID: view.ID, WorkspacePolicy: &view}}, store.Revision(), pending, workspacePolicyEvidence)
}
