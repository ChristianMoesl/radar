package agentservice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"radar/internal/agentruntime"
	"radar/internal/protocol"
)

func TestSuppliedRuntimeStartsCleanDefinitionAndPreservesStoppedState(t *testing.T) {
	dir := agentRuntimeTestDir(t)
	root := filepath.Join(dir, "agents")
	if _, err := agentruntime.Ensure(root); err != nil {
		if os.Getenv("RADAR_TEST_SUPPLIED_RUNTIME") == "1" {
			t.Fatal(err)
		}
		t.Skip("make agent-runtime is required for the supplied-runtime integration fixture: " + err.Error())
	}
	t.Setenv("RADAR_AGENT_ROOT", root)
	t.Setenv("RADAR_AGENT_HOST", "")
	t.Setenv("RADAR_SOCKET", filepath.Join(dir, "radar.sock"))
	project, workspace := t.TempDir(), t.TempDir()
	_, file, _, _ := runtime.Caller(0)
	source, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../agent-host/examples/counter.ts"))
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(project, "agent.ts")
	source = []byte(`import { radar } from "@christianmoesl/radar-agent/tools";
if (typeof radar !== "function") throw Error("SDK subpath unavailable");
` + strings.ReplaceAll(string(source), `"../src/index.ts"`, `"@christianmoesl/radar-agent"`))
	if err := os.WriteFile(entry, source, 0600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "radar-helper")
	binding := protocol.AgentWorkspaceBinding{ID: "fixture", Path: workspace, Mode: "local"}
	wire, _ := json.Marshal(binding)
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' '"+string(wire)+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_BINARY", helper)
	manager := New(workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) { return binding, nil }})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	call := func(req protocol.AgentRequest) json.RawMessage {
		t.Helper()
		value, err := manager.Handle(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, id := range []string{"counter", "stopped"} {
		call(protocol.AgentRequest{Operation: "add", ID: id, Entrypoint: entry, Workspace: workspace})
	}
	t.Cleanup(func() {
		_, _ = manager.Handle(context.Background(), protocol.AgentRequest{Operation: "stop", ID: "counter"})
	})
	_, args, err := hostCommand()
	if err != nil || len(args) != 1 || !strings.HasPrefix(args[0], filepath.Join(root, ".runtime")+string(filepath.Separator)) {
		t.Fatalf("runner is not supplied by Radar: %v, %v", args, err)
	}
	call(protocol.AgentRequest{Operation: "start", ID: "counter"})
	event := protocol.AgentRequest{Operation: "emit", ID: "counter", Params: json.RawMessage(`{"source":"fixture","id":"one","payload":null}`)}
	receipt := call(event)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var s protocol.AgentStatus
		_ = json.Unmarshal(call(protocol.AgentRequest{Operation: "inspect", ID: "counter"}), &s)
		if s.Snapshot != nil && s.Snapshot.Presentation.Summary == "1 events received" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("supplied SDK task did not complete: %+v", s)
		}
		time.Sleep(25 * time.Millisecond)
	}
	call(protocol.AgentRequest{Operation: "stop", ID: "counter"})
	call(protocol.AgentRequest{Operation: "start", ID: "counter"})
	if again := call(event); string(again) != string(receipt) {
		t.Fatal("runtime restart reset durable admission")
	}
	runCtx, stop := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() { defer close(finished); manager.Run(runCtx, nil) }()
	time.Sleep(100 * time.Millisecond)
	stop()
	<-finished
	var stopped protocol.AgentStatus
	_ = json.Unmarshal(call(protocol.AgentRequest{Operation: "inspect", ID: "stopped"}), &stopped)
	if stopped.Desired || stopped.Running {
		t.Fatal("supervision started an intentionally stopped registration")
	}
	files, err := os.ReadDir(project)
	if err != nil || len(files) != 1 || files[0].Name() != "agent.ts" {
		t.Fatalf("definition project mutated: %v, %v", files, err)
	}
}
