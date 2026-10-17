package agentservice

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestRunnerLifecycleAndDurableDelivery(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node 24 is required")
	}
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	if _, err := os.Stat(filepath.Join(repo, "agent-host/node_modules/@earendil-works/pi-durable")); err != nil {
		t.Skip("install agent-host dependencies to run runner integration tests")
	}
	dir := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(dir, "agents"))
	t.Setenv("RADAR_SOCKET", filepath.Join(dir, "radar.sock"))
	t.Setenv("RADAR_AGENT_HOST", filepath.Join(repo, "agent-host/src/runner.ts"))
	helper := filepath.Join(dir, "radar-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' '{\"id\":\"fixture\",\"path\":\""+workspace+"\",\"mode\":\"local\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_BINARY", helper)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager := New(func(_ context.Context, path string) (protocol.AgentWorkspaceBinding, error) {
		return protocol.AgentWorkspaceBinding{ID: "fixture", Path: workspace, Mode: "local"}, nil
	})
	request := func(op string, params string) json.RawMessage {
		t.Helper()
		value, err := manager.Handle(ctx, protocol.AgentRequest{Operation: op, ID: "counter", Params: json.RawMessage(params)})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	_, err := manager.Handle(ctx, protocol.AgentRequest{Operation: "add", ID: "counter", Workspace: workspace, Entrypoint: filepath.Join(repo, "agent-host/examples/counter.ts")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = manager.Handle(context.Background(), protocol.AgentRequest{Operation: "stop", ID: "counter"})
	}()
	request("start", "")
	event := `{"source":"fixture","id":"one","payload":{"value":1}}`
	first := request("emit", event)
	second := request("emit", event)
	if string(first) != string(second) {
		t.Fatalf("duplicate receipt changed: %s / %s", first, second)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status protocol.AgentStatus
		_ = json.Unmarshal(request("inspect", ""), &status)
		if status.Snapshot != nil && status.Snapshot.Presentation.Summary == "1 events received" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not run: %+v", status)
		}
		time.Sleep(25 * time.Millisecond)
	}
	request("stop", "")
	if _, err := manager.Handle(ctx, protocol.AgentRequest{Operation: "emit", ID: "counter", Params: json.RawMessage(event)}); err == nil {
		t.Fatal("stopped agent accepted work")
	}
	request("start", "")
	third := request("emit", event)
	if string(first) != string(third) {
		t.Fatal("receipt changed after restart")
	}
	if _, err := manager.Handle(ctx, protocol.AgentRequest{Operation: "emit", ID: "counter", Params: json.RawMessage(strings.Replace(event, `"value":1`, `"value":2`, 1))}); err == nil {
		t.Fatal("changed content accepted")
	}
}
