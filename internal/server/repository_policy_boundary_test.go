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
	"radar/internal/protocol"
	"radar/internal/state"
)

type invalidRepositoryPolicySource struct {
	repositoryDiagnosticSource
	view protocol.DiagnosticRepositoryPolicy
}

func (p invalidRepositoryPolicySource) DiagnosticRepositoryPolicy(_ context.Context, parent, id, scope string) (protocol.DiagnosticRepositoryPolicy, error) {
	*p.calls++
	if scope != "/registered-scope" {
		panic("scope widened")
	}
	view := p.view
	view.ID, view.WorkspaceID, view.Origin = id, parent, "current_registered_member_repository"
	return view, nil
}

func TestRepositoryPolicyRPCWithholdsInvalidNativePayload(t *testing.T) {
	private := "private-provider-policy-and-command"
	for name, view := range map[string]protocol.DiagnosticRepositoryPolicy{
		"private_setting":     {Available: true, Evidence: "fresh_bounded_repository_configuration", Settings: []protocol.DiagnosticSetting{{Key: "model", Value: private}}},
		"private_reason":      {Evidence: "unavailable", Reason: private, Settings: []protocol.DiagnosticSetting{}},
		"private_evidence":    {Evidence: private, Reason: "repository_unavailable", Settings: []protocol.DiagnosticSetting{}},
		"unavailable_payload": {Evidence: "unavailable", Reason: "repository_unavailable", Settings: []protocol.DiagnosticSetting{{Key: private}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			store, err := state.NewStore(logger)
			if err != nil {
				t.Fatal(err)
			}
			calls, effects := 0, 0
			provider := invalidRepositoryPolicySource{repositoryDiagnosticSource: repositoryDiagnosticSource{calls: &calls, effects: &effects}, view: view}
			s := New(store, logger, func() { effects++ }, func() error { effects++; return nil }, nil, integration.NewRegistry(provider), cleanup.New(nil))
			req := protocol.DiagnosticsRequest{Section: "repository_policy", ParentID: "registered", ID: "workspace-resource:" + strings.Repeat("a", 64), Workspace: "/registered-scope"}
			revision := store.Revision()
			response := managedPipeCall(t, s, protocol.Request{Method: "diagnostics-probe", Diagnostics: &req})
			encoded, err := json.Marshal(response)
			if err != nil || response.OK || response.Diagnostics != nil || strings.Contains(string(encoded), private) || calls != 1 || effects != 0 || store.Revision() != revision {
				t.Fatal("RPC published invalid private evidence or changed execution boundary")
			}
		})
	}
}
