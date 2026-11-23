package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/state"
)

type manualDiagnosticProvider struct {
	previews int
	path     string
}

func (*manualDiagnosticProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "tmux"}
}
func (*manualDiagnosticProvider) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	panic("collection")
}
func (*manualDiagnosticProvider) Cleanup(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error) {
	panic("execution")
}
func (p *manualDiagnosticProvider) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if req.Mode != integration.CleanupConfirmed {
		panic("wrong mode")
	}
	p.previews++
	return []protocol.CleanupTarget{{Source: "tmux", Kind: "session", ResourceID: "private-session", Description: "private-description", Path: p.path}}, nil
}

func TestManualCleanupInventoryRPCIsRejectedBeforeProviderReads(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config")
	root := filepath.Join(home, "workspaces")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("RADAR_STATE", filepath.Join(home, "state.db"))
	agents := filepath.Join(home, "agents")
	t.Setenv("RADAR_AGENT_ROOT", agents)
	if err := os.MkdirAll(filepath.Join(agents, "private-owner"), 0700); err != nil {
		t.Fatal(err)
	}
	owner, _ := json.Marshal(agentservice.Registration{Version: 1, ID: "private-owner", Desired: false, Entrypoint: "private-command", Workspace: protocol.AgentWorkspaceBinding{ID: "private-workspace", Path: root, Mode: "local"}})
	if err := os.WriteFile(filepath.Join(agents, "private-owner", "registration.json"), owner, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(config, "radar"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "radar", "config.yaml"), []byte("workspace: {root_dir: "+root+"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Attention: "done", SourceRefs: []protocol.SourceRef{{ID: "tmux:private-session", Source: "tmux", Kind: "session", Title: "private-session"}}}})
	records, _, revision := store.InspectionSnapshot()
	if len(records) != 1 {
		t.Fatal("fixture")
	}
	provider := &manualDiagnosticProvider{path: root}
	effects := 0
	pending := false
	server := New(store, logger, func() { effects++ }, func() error { effects++; return nil }, nil, integration.NewRegistry(workspace.Source{}, provider), cleanup.New([]integration.CleanupProvider{provider})).SetResourcePublication(func() { effects++ }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: pending} })
	host, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.handle(host) }()
	defer client.Close()
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	for i, req := range []protocol.Request{
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "cleanup_preview"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID, State: "done"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID, Workspace: filepath.Join(root, "other")}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID, CleanupMode: "expired", WorkspaceID: "not-registered"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID}},
	} {
		if i == 6 {
			pending = true
		}
		if err := encoder.Encode(req); err != nil {
			t.Fatal(err)
		}
		var response protocol.Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.OK || response.Diagnostics != nil || response.DiagnosticCleanup != nil || response.CleanupPreview != nil {
			t.Fatalf("obsolete manual/invalid expired inventory route accepted: %+v", response)
		}
		encoded, _ := json.Marshal(response)
		if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), root) {
			t.Fatal("private target/owner data escaped rejected RPC")
		}
	}
	client.Close()
	<-done
	if provider.previews != 0 || effects != 0 || store.Revision() != revision {
		t.Fatal("invalid preview reached providers or mutated state")
	}
}
