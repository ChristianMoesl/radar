package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/state"
)

type repositoryDiagnosticSource struct {
	workspace.Source
	calls   *int
	effects *int
}

func (p repositoryDiagnosticSource) DiagnosticRepositoryPolicy(_ context.Context, parent, id, scope string) (protocol.DiagnosticRepositoryPolicy, error) {
	*p.calls++
	if scope != "/registered-scope" {
		panic("scope widened")
	}
	return protocol.DiagnosticRepositoryPolicy{ID: id, WorkspaceID: parent, Origin: "current_registered_member_repository", Available: false, Evidence: "unavailable", Reason: "repository_unavailable", Settings: []protocol.DiagnosticSetting{}}, nil
}
func (p repositoryDiagnosticSource) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	*p.effects++
	panic("inventory fallback")
}
func TestRepositoryPolicyRPCRequiresExplicitMethodAndPreservesNativeBoundary(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	calls, effects := 0, 0
	provider := repositoryDiagnosticSource{calls: &calls, effects: &effects}
	s := New(store, logger, func() { effects++ }, func() error { effects++; return nil }, nil, integration.NewRegistry(provider), cleanup.New(nil))
	id := "workspace-resource:" + strings.Repeat("a", 64)
	req := protocol.DiagnosticsRequest{Section: "repository_policy", ParentID: "registered", ID: id, Workspace: "/registered-scope"}
	revision := store.Revision()
	if response := managedPipeCall(t, s, protocol.Request{Method: "diagnostics", Diagnostics: &req}); response.OK || calls != 0 {
		t.Fatal("implicit repository read accepted")
	}
	response := managedPipeCall(t, s, protocol.Request{Method: "diagnostics-probe", Diagnostics: &req})
	if !response.OK || response.Diagnostics == nil || response.Diagnostics.Schema != protocol.DiagnosticsVersion || response.Diagnostics.Total != 1 || calls != 1 || effects != 0 || store.Revision() != revision {
		t.Fatalf("RPC boundary changed: %+v calls=%d effects=%d", response, calls, effects)
	}
	view := response.Diagnostics.Items[0].RepositoryPolicy
	if view == nil || view.Available || view.Reason != "repository_unavailable" || len(view.Settings) != 0 {
		t.Fatal("unavailable became absent")
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), "/registered-scope") {
		t.Fatal("scope path leaked")
	}
}
