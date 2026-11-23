package diagnostics

import (
	"context"
	"errors"
	"path/filepath"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

const recreationPreviewEvidence = "explicit_native_exact_workspace_recreation_proposal"

// PreviewRecreation invokes only the native exact registered planner, never its
// executor. The fresh pending callback brackets potentially slow native reads.
func PreviewRecreation(ctx context.Context, store *state.Store, req protocol.DiagnosticsRequest, pending func() bool, providers integration.Registry) (protocol.DiagnosticsPage, error) {
	fail := func() (protocol.DiagnosticsPage, error) {
		return protocol.DiagnosticsPage{}, errors.New("native exact workspace recreation preview unavailable; no recreation requested")
	}
	if req.Section != "recreation_preview" || req.ParentID == "" || len(req.ParentID) > 512 || req.ID != "" || req.State != "" || req.Cursor != "" || req.CleanupMode != "" || req.WorkspaceID != "" || req.Limit < 0 || req.Limit > 50 || len(req.Workspace) > 4096 || (req.Workspace != "" && !filepath.IsAbs(req.Workspace)) || pending == nil {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, CleanupPreviewTimeout)
	defer cancel()
	if previewExpired(ctx) || pending() {
		return fail()
	}
	provider, err := providers.WorkspaceDiagnostics()
	if err != nil {
		return fail()
	}
	planner, ok := provider.(integration.SandboxRecreationPreviewDiagnostics)
	if !ok {
		return fail()
	}
	revision := store.Revision()
	view, err := planner.DiagnosticRecreationPreview(ctx, req.ParentID, req.Workspace)
	if err != nil || !protocol.ValidDiagnosticRecreationPreview(view, req.ParentID) || previewExpired(ctx) || pending() || store.Revision() != revision {
		return fail()
	}
	req.Limit = 1
	return paginate(req, []protocol.DiagnosticItem{{ID: view.ID, RecreationPreview: &view}}, revision, false, recreationPreviewEvidence)
}
