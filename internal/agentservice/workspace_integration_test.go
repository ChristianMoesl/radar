package agentservice_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
)

func TestManagedWorkspaceNativeLifecycleAndRetainedAdmission(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node 24+ required")
	}
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	if _, err := os.Stat(filepath.Join(repo, "agent-host/node_modules/@earendil-works/pi-durable")); err != nil {
		t.Skip("install agent-host dependencies")
	}
	short, err := os.MkdirTemp("", "raw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	root, configHome := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(configHome, "radar"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.yaml"), []byte(fmt.Sprintf("workspace:\n  root_dir: %q\n  auto_confirm: true\nsbx:\n  enabled: false\n", root)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	t.Setenv("RADAR_SOCKET", filepath.Join(short, "radar.sock"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(short, "agents"))
	t.Setenv("RADAR_AGENT_HOST", filepath.Join(repo, "agent-host/src/runner.ts"))
	provider := &recoveringWorkspace{AgentWorkspaceManager: workspace.NewSource(obsidian.NewSourceAt(t.TempDir()))}
	manager := agentservice.New(provider)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	call := func(req protocol.AgentRequest) json.RawMessage {
		t.Helper()
		value, err := manager.Handle(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	status := func(op string) protocol.AgentStatus {
		t.Helper()
		var value protocol.AgentStatus
		if err := json.Unmarshal(call(protocol.AgentRequest{ID: "counter", Operation: op}), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	add := protocol.AgentRequest{ID: "counter", Operation: "add", Entrypoint: filepath.Join(repo, "agent-host/examples/counter.ts")}
	var added protocol.AgentStatus
	if err := json.Unmarshal(call(add), &added); err != nil {
		t.Fatal(err)
	}
	if added.Workspace.Path == "" || filepath.Dir(added.Workspace.Path) != root || added.Workspace.Mode != "local" || added.Running || added.Desired {
		t.Fatalf("invalid provisioning: %+v", added)
	}
	helper := filepath.Join(short, "radar-helper")
	binding, _ := json.Marshal(added.Workspace)
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' '"+string(binding)+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_BINARY", helper)
	t.Cleanup(func() {
		_, _ = manager.Handle(context.Background(), protocol.AgentRequest{Operation: "stop", ID: "counter"})
	})
	status("start")
	event := protocol.AgentRequest{ID: "counter", Operation: "emit", Params: json.RawMessage(`{"source":"fixture","id":"one","payload":{"value":1}}`)}
	receipt := call(event)
	deadline := time.Now().Add(5 * time.Second)
	for {
		inspected := status("inspect")
		if inspected.Snapshot != nil && inspected.Snapshot.Presentation.Summary == "1 events received" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("native task did not persist result: %+v", inspected)
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Losing the workspace while Node is still alive must stop that owner,
	// repair through the lifecycle provider, and then resume native work.
	oldPID := status("inspect").Snapshot.PID
	provider.unavailable.Store(true)
	runCtx, stopSupervisor := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() { defer close(finished); manager.Run(runCtx, nil) }()
	deadline = time.Now().Add(5 * time.Second)
	for {
		inspected := status("inspect")
		if inspected.Running && inspected.Error == "" && inspected.Snapshot.PID != oldPID && provider.repairs.Load() > 0 {
			break
		}
		if time.Now().After(deadline) {
			stopSupervisor()
			<-finished
			t.Fatalf("running owner was not recovered: %+v", inspected)
		}
		time.Sleep(25 * time.Millisecond)
	}
	stopSupervisor()
	<-finished
	// A replacement manager reconnects; explicit restart reopens the native store.
	manager = agentservice.New(provider)
	status("restart")
	if repeated := call(event); string(receipt) != string(repeated) {
		t.Fatal("receipt changed after restart")
	}
	status("stop")
	call(protocol.AgentRequest{Operation: "remove", ID: "counter"})
	for _, path := range []string{added.Workspace.Path, filepath.Join(added.Workspace.Path, "notes.md"), filepath.Join(added.DataDir, "workspace.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("removal destroyed %s: %v", path, err)
		}
	}
	// Removal/restoration needs no workspace selection and retains native receipts.
	var restored protocol.AgentStatus
	if err := json.Unmarshal(call(add), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Workspace != added.Workspace {
		t.Fatal("restoration created another workspace")
	}
	status("start")
	if repeated := call(event); string(receipt) != string(repeated) {
		t.Fatal("receipt changed after restoration")
	}
	status("stop")
}

// Model a failed workspace observation without requiring a live SBX provider.
// Resource-level stopped/missing SBX behavior has separate planner fixtures.
type recoveringWorkspace struct {
	integration.AgentWorkspaceManager
	unavailable atomic.Bool
	repairs     atomic.Int32
}

func (w *recoveringWorkspace) AgentWorkspace(ctx context.Context, path string) (protocol.AgentWorkspaceBinding, error) {
	if w.unavailable.Load() {
		return protocol.AgentWorkspaceBinding{}, errors.New("fixture runtime unavailable")
	}
	return w.AgentWorkspaceManager.AgentWorkspace(ctx, path)
}
func (w *recoveringWorkspace) PrepareAgentWorkspace(ctx context.Context, req integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
	if req.Expected != nil && w.unavailable.Swap(false) {
		w.repairs.Add(1)
	}
	return w.AgentWorkspaceManager.PrepareAgentWorkspace(ctx, req)
}
