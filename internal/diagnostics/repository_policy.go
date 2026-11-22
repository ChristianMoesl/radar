package diagnostics

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

const repositoryPolicyEvidence = "explicit_current_registered_member_repository_declarations"

var repositoryMemberID = regexp.MustCompile(`^workspace-resource:[a-f0-9]{64}$`)

func repositoryPolicy(ctx context.Context, store *state.Store, req protocol.DiagnosticsRequest, pending bool, providers integration.Registry) (protocol.DiagnosticsPage, error) {
	if req.ParentID == "" || len(req.ParentID) > 512 || !repositoryMemberID.MatchString(req.ID) || req.State != "" || req.Cursor != "" || req.Limit < 0 || req.Limit > 50 || len(req.Workspace) > 4096 || (req.Workspace != "" && !filepath.IsAbs(req.Workspace)) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy requires one exact registered workspace parent/member ID and no state/cursor selectors")
	}
	provider, err := providers.WorkspaceDiagnostics()
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy registration capability unavailable")
	}
	reader, ok := provider.(integration.RepositoryPolicyDiagnostics)
	if !ok {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy diagnostics unsupported")
	}
	ctx, cancel := context.WithTimeout(ctx, RuntimeProbeTimeout)
	defer cancel()
	view, err := reader.DiagnosticRepositoryPolicy(ctx, req.ParentID, req.ID, req.Workspace)
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy registration unavailable in selected scope")
	}
	if view.ID != req.ID || view.WorkspaceID != req.ParentID || view.Origin != "current_registered_member_repository" {
		return protocol.DiagnosticsPage{}, fmt.Errorf("repository policy evidence invalid")
	}
	req.Limit = 1
	return paginate(req, []protocol.DiagnosticItem{{ID: view.ID, RepositoryPolicy: &view}}, store.Revision(), pending, repositoryPolicyEvidence)
}
