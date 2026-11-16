package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestAgentProbeSingleTargetScopeUnknownAndNoImplicitRead(t *testing.T) {
	store, _, workspace := fixture(t)
	dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "worker")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg := agentservice.Registration{Version: 1, ID: "worker", Workspace: protocol.AgentWorkspaceBinding{ID: "workspace", Path: workspace, Mode: "local"}}
	raw, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(dir, "registration.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	short, err := os.MkdirTemp("", "ap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	t.Setenv("RADAR_SOCKET", filepath.Join(short, "r"))
	req := protocol.DiagnosticsRequest{Section: "agent_probe", ParentID: "worker", Workspace: workspace, Limit: 50}
	page, err := Probe(context.Background(), store, req, true, integration.NewRegistry())
	if err != nil || page.Total != 1 || len(page.Items) != 1 || !page.Complete || page.NextCursor != "" || !page.ResourceRefreshPending || page.Schema != protocol.DiagnosticsVersion || page.Evidence != "explicit_agent_socket_probe_no_checkpoint_fallback" || page.Items[0].AgentProbe.RunnerEvidence != "unknown_not_reached" {
		t.Fatalf("%+v %v", page, err)
	}
	if _, err := Read(store, req, false, integration.NewRegistry()); err == nil {
		t.Fatal("cached RPC accepted explicit agent probe")
	}
	for _, change := range []func(*protocol.DiagnosticsRequest){
		func(r *protocol.DiagnosticsRequest) { r.ParentID = "" }, func(r *protocol.DiagnosticsRequest) { r.ID = "worker" }, func(r *protocol.DiagnosticsRequest) { r.State = "done" }, func(r *protocol.DiagnosticsRequest) { r.Cursor = "cursor" }, func(r *protocol.DiagnosticsRequest) { r.Workspace = workspace + "-other" }, func(r *protocol.DiagnosticsRequest) { r.Workspace = "relative" }, func(r *protocol.DiagnosticsRequest) { r.Limit = 51 }, func(r *protocol.DiagnosticsRequest) { r.ParentID = "../worker" },
	} {
		invalid := req
		change(&invalid)
		if _, err := Probe(context.Background(), store, invalid, false, integration.NewRegistry()); err == nil {
			t.Fatalf("invalid selector accepted: %+v", invalid)
		}
	}
}

func TestAgentProbeRejectsDaemonRevisionDrift(t *testing.T) {
	store, _, workspace := fixture(t)
	dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "worker")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg := agentservice.Registration{Version: 1, ID: "worker", Workspace: protocol.AgentWorkspaceBinding{ID: "workspace", Path: workspace, Mode: "local"}}
	raw, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(dir, "registration.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	short, err := os.MkdirTemp("", "ap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	t.Setenv("RADAR_SOCKET", filepath.Join(short, "r"))
	sum := sha256.Sum256([]byte(reg.ID))
	path := filepath.Join(short, base64.RawURLEncoding.EncodeToString(sum[:8]))
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request any
		_ = json.NewDecoder(conn).Decode(&request)
		store.SetTasks([]protocol.Task{task(workspace, "test:ABC-123")})
		_ = json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocol": 99, "instanceId": "worker"}})
	}()
	if _, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "agent_probe", ParentID: "worker"}, false, integration.NewRegistry()); err == nil {
		t.Fatal("daemon revision drift accepted")
	}
	<-done
}
