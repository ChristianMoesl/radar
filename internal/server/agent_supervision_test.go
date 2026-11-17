package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/integration"
	agentintegration "radar/internal/integration/agent"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestDiagnosticsRPCAgentSupervisionUsesNativeMemoryWithoutRuntimeCalls(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RADAR_STATE", filepath.Join(root, "state.db"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	dir := filepath.Join(root, "agents", "stopped")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg := agentservice.Registration{Version: 1, ID: "stopped", Workspace: protocol.AgentWorkspaceBinding{ID: "backing", Path: filepath.Join(root, "workspace"), Mode: "local"}}
	raw, _ := json.Marshal(reg)
	path := filepath.Join(dir, "registration.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	// The actual manager has a nil workspace dependency, so status/probe/repair
	// would panic. Its diagnostics must read only memory and registration files.
	source := agentintegration.NewSource(nil)
	mutations := 0
	s := New(store, logger, func() { mutations++ }, nil, nil, integration.NewRegistry(source), cleanup.New(nil))
	host, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); s.handle(host) }()
	defer func() { client.Close(); <-done }()
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	for _, scope := range []string{"", reg.Workspace.Path, reg.Workspace.Path + "-other"} {
		if err := encoder.Encode(protocol.Request{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "agents", Workspace: scope, ID: reg.ID}}); err != nil {
			t.Fatal(err)
		}
		var response protocol.Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if !response.OK || response.Diagnostics == nil || response.Diagnostics.Schema != protocol.DiagnosticsVersion || response.Tasks != nil || response.Sources != nil {
			t.Fatalf("unexpected response: %+v", response)
		}
		if scope == reg.Workspace.Path+"-other" {
			if response.Diagnostics.Total != 0 {
				t.Fatal("scope escaped")
			}
			continue
		}
		if response.Diagnostics.Total != 1 {
			t.Fatal("stopped registration missing")
		}
		view := response.Diagnostics.Items[0].Agent
		if view.SupervisionEvidence != "current_daemon_retry_and_schedule_errors" || view.Supervision == nil || view.Supervision.RetryRecordEvidence != "no_current_record" || view.Supervision.Retry != nil || view.Desired || view.RunnerEvidence != "unknown_not_probed" || view.LeaseEvidence != "unknown_not_inspected" || view.OwnershipHold != "registered_owner_even_when_stopped" {
			t.Fatalf("conflated native evidence: %+v", view)
		}
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) || mutations != 0 {
		t.Fatal("diagnostic RPC mutated state")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatal("diagnostics wrote checkpoints/runtime state")
	}
}
