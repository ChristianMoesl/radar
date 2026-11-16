package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestRetirementPreviewRPCRequiresExplicitMethodAndExactScopeWithoutMutation(t *testing.T) {
	home := t.TempDir()
	root, config := filepath.Join(home, "workspaces"), filepath.Join(home, "config")
	path := filepath.Join(root, "retained")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("RADAR_STATE", filepath.Join(home, "state.db"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(home, "agents"))
	if err := os.MkdirAll(filepath.Join(config, "radar"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "radar", "config.yaml"), []byte("workspace: {root_dir: "+root+"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "retained", Path: path, SessionName: "registered-session", Sandbox: &workspacegroup.Sandbox{Name: "registered-sandbox", Agent: "shell"}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	ownerDir := filepath.Join(home, "agents", "stopped")
	if err := os.MkdirAll(ownerDir, 0700); err != nil {
		t.Fatal(err)
	}
	owner, err := json.Marshal(agentservice.Registration{Version: 1, ID: "stopped", Desired: false, Workspace: protocol.AgentWorkspaceBinding{ID: group.ID, Path: path, Mode: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownerDir, "registration.json"), owner, 0600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Attention: "done", Title: "private-title", SourceRefs: []protocol.SourceRef{{ID: "test:ABC-123", EntityID: "test:ABC-123", Source: "test", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", Metadata: map[string]string{"completed_at": time.Now().Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339)}}, {ID: "workspace:" + group.ID, Source: "workspace", Kind: "workspace", Path: path, WorkspaceID: group.ID, ProvidesWorkspace: true}}}})
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture should be UI-hidden")
	}
	revision, effects := store.Revision(), 0
	trap := diagnosticNoCleanup{fakeCleanupSource: fakeCleanupSource{name: "sbx"}, calls: &effects}
	server := New(store, logger, func() { effects++ }, func() error { effects++; return nil }, nil, integration.NewRegistry(workspace.Source{}, trap), cleanup.New([]integration.CleanupProvider{trap})).SetResourcePublication(func() { effects++ }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: true} })
	host, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.handle(host) }()
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	for index, req := range []protocol.Request{
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "retirement_preview"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID, Workspace: path + "-sibling"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID, Cursor: "cursor"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID, Workspace: path}},
	} {
		if err := encoder.Encode(req); err != nil {
			t.Fatal(err)
		}
		var response protocol.Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if index < 4 {
			if response.OK || response.Diagnostics != nil {
				t.Fatalf("implicit/unscoped preview accepted: %+v", response)
			}
		} else {
			if !response.OK || response.Diagnostics == nil || response.Diagnostics.Total != 1 || response.Diagnostics.Schema != protocol.DiagnosticsVersion || !response.Diagnostics.ResourceRefreshPending || len(response.Diagnostics.Items[0].RetirementPreview.Targets) != 3 {
				t.Fatalf("explicit proposal missing: %+v", response)
			}
			view := response.Diagnostics.Items[0].RetirementPreview
			if view.OwnershipEvidence != "fresh_bounded_registration" || view.AgentOwnerCount == nil || *view.AgentOwnerCount != 1 || view.ExecutionEligibility != "blocked_registered_agent" {
				t.Fatalf("stopped-owner execution block lost across RPC: %+v", view)
			}
		}
	}
	_ = client.Close()
	<-done
	if effects != 0 || store.Revision() != revision {
		t.Fatal("preview called cleanup/collection/refresh or mutated cache")
	}
}
