package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"radar/internal/client"
	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestExternalClientOperationIdentityReachesTUIThroughRevisionWatch(t *testing.T) {
	f := newReactivityBaselineFixture(t)
	task := f.model.tasks[0]
	workspaceID := task.SourceRefs[0].WorkspaceID
	reported, err := client.ReportResourceMutation(f.model.socketPath, protocol.ResourceMutation{TaskID: task.ID, WorkspaceID: workspaceID})
	if err != nil || !reported.OK {
		t.Fatalf("external CLI/Pi notification failed: %+v %v", reported, err)
	}
	msg := f.model.watch(f.model.revision)().(watchMsg)
	f.model = updateOperation(f.model, msg)
	if len(f.model.managedOperations) != 1 || f.model.managedOperations[0].ID != reported.Operations[0].ID || !f.model.resourceRefreshPending {
		t.Fatalf("watch lost report identity/pending: %+v", f.model.managedOperations)
	}
	if detail := ansi.Strip(f.model.operationDetails(task, 120)); !strings.Contains(detail, "not daemon execution") {
		t.Fatalf("client report implied execution: %s", detail)
	}
	old := msg
	id := f.store.BeginOperation("cleanup", task.ID, workspaceID)
	f.model = updateOperation(f.model, f.model.watch(f.model.revision)())
	if op, ok := f.model.managedOperation(task); !ok || op.ID != id || op.Status != "running" {
		t.Fatal("daemon-confirmed running operation not published to its own row")
	}
	// Local spinners are request feedback. Watch traffic must not finish or
	// retarget them, even when a different client completes daemon execution.
	f.model.startOperation(task, "cleanup", "Cleaning up…", "Cleanup failed", nil)
	f.store.FinishOperation(id, protocol.ManagedOperation{Status: "failed", TaskID: task.ID})
	f.model = updateOperation(f.model, f.model.watch(f.model.revision)())
	detail := ansi.Strip(f.model.operationDetails(task, 120))
	if !strings.Contains(detail, "Local request:") || !strings.Contains(detail, "Daemon: cleanup failed") || !strings.Contains(detail, id) || f.model.cleanupOperation.kind == "" {
		t.Fatalf("local feedback confused with confirmed result: %s", detail)
	}
	f.model = updateOperation(f.model, old)
	if op, _ := f.model.managedOperation(task); op.ID != id || op.Status != "failed" {
		t.Fatal("stale report replaced newer operation evidence")
	}
	f.service.Refresh(context.Background(), true)
	f.model = updateOperation(f.model, f.model.watch(f.model.revision)())
	if f.model.resourceRefreshPending {
		t.Fatal("authoritative reread left the TUI pending")
	}
	if op, _ := f.model.managedOperation(protocol.Task{ID: task.ID + 1}); op.ID != "" {
		t.Fatal("operation leaked onto unrelated task")
	}
}

func TestWorkspaceResultDoesNotUndoAlreadyReconciledPublication(t *testing.T) {
	m := operationFixture()
	m.resourceRefreshPending = true
	m.revision = 7
	m.startOperation(m.tasks[0], "workspace", "Updating workspace…", "Workspace update failed", nil)
	response := protocol.Response{OK: true, Revision: 8, Tasks: m.tasks, Sources: []protocol.SourceStatus{}}
	msg := workspaceAppliedMsg{result: integration.WorkspaceReconcileResult{OK: true}, publication: &actionMsg{response: &response}}
	m = updateOperation(m, msg)
	if m.resourceRefreshPending || m.revision != 8 || m.operation.kind != "" {
		t.Fatal("workspace completion overwrote the authoritative fresh snapshot")
	}
}
