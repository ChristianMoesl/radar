package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestAgentPresentationKeepsRuntimeAttentionSummaryAndErrorsSeparate(t *testing.T) {
	status := protocol.AgentStatus{ID: "watcher", DataDir: t.TempDir(), Running: true, Error: "native runtime failed", CollectionError: "delivery pending", ScheduleError: "schedule registry unreadable", Workspace: protocol.AgentWorkspaceBinding{ID: "ordinary", Path: "/work/ordinary", Mode: "local"}, Snapshot: &protocol.AgentSnapshot{Pending: 0, Problem: "snapshot unavailable", Presentation: protocol.AgentPresentation{Title: "Health watcher", Attention: true, Summary: "Review ABC-123 findings"}}, Schedules: []protocol.AgentScheduleStatus{{Error: "admission failed"}}}
	metadata := agentPresentationMetadata(status)
	if metadata["running"] != "true" || metadata["attention"] != "true" || metadata["summary"] != "Review ABC-123 findings" || metadata["workspace_id"] != "ordinary" {
		t.Fatalf("definition and runtime facts conflated: %+v", metadata)
	}
	for _, want := range []string{status.Error, status.CollectionError, status.ScheduleError, status.Snapshot.Problem, status.Schedules[0].Error} {
		if !strings.Contains(metadata["runtime_error"], want) {
			t.Fatalf("error %q hidden", want)
		}
	}
	if metadata["owned_workspace_id"] != "" {
		t.Fatal("explicit binding falsely marked owned")
	}
	status.Running = false
	status.Snapshot = nil
	status.Error = ""
	status.CollectionError = ""
	status.ScheduleError = ""
	status.Schedules = nil
	metadata = agentPresentationMetadata(status)
	if metadata["running"] != "false" || metadata["attention"] != "" || metadata["runtime_error"] != "" || metadata["summary"] != "" {
		t.Fatalf("stopped registration lost or state fabricated: %+v", metadata)
	}
}

func TestAgentPresentationOwnershipFailureIsVisibleAndDoesNotHideOrdinaryWork(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(`{"version":999}`), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := agentPresentationMetadata(protocol.AgentStatus{ID: "watcher", DataDir: dir, Workspace: protocol.AgentWorkspaceBinding{ID: "ordinary", Path: "/work/ordinary"}})
	if metadata["owned_workspace_id"] != "" || metadata["runtime_error"] == "" {
		t.Fatalf("uncertain ownership hidden or inferred: %+v", metadata)
	}
}

type stopWorkspaceFixture struct {
	binding  protocol.AgentWorkspaceBinding
	prepares int
}

func (f *stopWorkspaceFixture) AgentWorkspace(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
	return f.binding, nil
}
func (f *stopWorkspaceFixture) PrepareAgentWorkspace(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
	f.prepares++
	return protocol.AgentWorkspaceBinding{}, integration.WorkspaceReconcilePlan{}, errors.New("stop must not prepare a workspace")
}

func TestAgentStopSourceDelegatesGuardedNativeCaptureWithoutGenericControl(t *testing.T) {
	for _, kind := range []string{"allowed", "denied", "nil_guard", "binding_drift"} {
		t.Run(kind, func(t *testing.T) {
			root, err := os.MkdirTemp("", "rs-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			binding := protocol.AgentWorkspaceBinding{ID: "workspace-worker", Path: filepath.Join(root, "workspace"), Mode: "local"}
			reg := agentservice.Registration{Version: 1, ID: "worker", Entrypoint: "private-definition", Desired: true, Workspace: binding}
			dir := filepath.Join(root, "agents", reg.ID)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(reg)
			path := filepath.Join(dir, "registration.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			workspace := &stopWorkspaceFixture{binding: binding}
			source := NewSource(workspace)
			provider, err := integration.NewRegistry(source).AgentStopDiagnostics()
			if err != nil || provider != source {
				t.Fatal("native source did not expose typed capability", err)
			}
			calls := 0
			guard := integration.AgentStopGuard(func(_ context.Context, execute func() error) error {
				calls++
				if kind == "denied" {
					return errors.New("private-scope-refusal")
				}
				return execute()
			})
			if kind == "nil_guard" {
				guard = nil
			}
			plan, err := provider.PrepareDiagnosticAgentStop(context.Background(), reg.ID, binding, guard)
			if kind == "nil_guard" {
				if err == nil || plan != nil {
					t.Fatal("source returned unguarded or typed-zero admission")
				}
				return
			}
			if err != nil || plan.Preview() != (integration.AgentStopPreview{ID: reg.ID, Desired: true, Running: false}) || calls != 0 || workspace.prepares != 0 {
				t.Fatal("native capture lost facts or performed admission/preparation", err)
			}
			encoded, _ := json.Marshal(plan)
			if string(encoded) != "{}" {
				t.Fatal("native plan serialized", string(encoded))
			}
			guard = func(context.Context, func() error) error { t.Error("caller replaced captured guard"); return nil }
			if kind == "binding_drift" {
				workspace.binding.ID += "-other"
			}
			err = plan.Execute(context.Background())
			if (kind == "allowed") != (err == nil) {
				t.Fatal("typed source bypassed/lost native admission", err)
			}
			if err := plan.Execute(context.Background()); err == nil {
				t.Fatal("source admission replayed")
			}
			after, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(after, &reg) != nil || reg.Desired != (kind != "allowed") || workspace.prepares != 0 {
				t.Fatal("source removed registration, changed refused intent or prepared workspace", err)
			}
			if calls != map[string]int{"allowed": 1, "denied": 1, "binding_drift": 0}[kind] {
				t.Fatal("unexpected final guard calls", calls)
			}
		})
	}
}
