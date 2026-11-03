package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
