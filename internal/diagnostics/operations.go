package diagnostics

import (
	"sort"

	"radar/internal/protocol"
	"radar/internal/state"
)

func operationItems(req protocol.DiagnosticsRequest, records []state.TaskRecord, refs []state.SourceRefRecord, operations []protocol.ManagedOperation) []protocol.DiagnosticItem {
	byRecord := map[string][]state.SourceRefRecord{}
	for _, ref := range refs {
		byRecord[ref.TaskRecordID] = append(byRecord[ref.TaskRecordID], ref)
	}
	tasks := map[int]bool{}
	workspaces := map[string]bool{}
	if req.Workspace != "" {
		for _, record := range records {
			tasks[record.NumericID] = recordInWorkspace(byRecord[record.ID], req.Workspace)
		}
		for _, ref := range refs {
			if ref.Snapshot.WorkspaceID != "" && recordInWorkspace([]state.SourceRefRecord{ref}, req.Workspace) {
				workspaces[ref.Snapshot.WorkspaceID] = true
			}
		}
	}
	items := []protocol.DiagnosticItem{}
	for _, op := range operations {
		if req.ID != "" && req.ID != op.ID {
			continue
		}
		// An explicit workspace target takes precedence over a related task:
		// a report for another workspace must not cross that boundary.
		if req.Workspace != "" && ((op.WorkspaceID != "" && !workspaces[op.WorkspaceID]) || (op.WorkspaceID == "" && !tasks[op.TaskID])) {
			continue
		}
		view := protocol.DiagnosticOperation{ID: clip(op.ID), Kind: "unknown", Status: "unknown", TaskID: op.TaskID, WorkspaceID: clip(op.WorkspaceID), ExecutionEvidence: "unknown_not_captured", TargetEvidence: "unknown_not_captured"}
		switch op.Kind {
		case "cleanup", "workspace", "task-create", "task-done", "task-reopen", "task-priority", "task-mute", "task-unmute", "task-title", "task-note", "task-delete":
			view.Kind = op.Kind
		}
		switch op.Status {
		case "running":
			view.Status, view.ExecutionEvidence = op.Status, "daemon_handler_running"
		case "succeeded", "failed", "partial":
			view.Status, view.ExecutionEvidence = op.Status, "daemon_handler_result_not_resource_verification"
			if op.Kind == "cleanup" && op.CompletedTargets >= 0 && op.TotalTargets >= op.CompletedTargets {
				view.TargetEvidence = "native_cleanup_successful_prefix_only"
				completed, total := op.CompletedTargets, op.TotalTargets
				view.CompletedTargets, view.TotalTargets = &completed, &total
			}
		case "reported":
			view.Status, view.ExecutionEvidence = op.Status, "unverified_client_report_not_daemon_execution"
		}
		items = append(items, protocol.DiagnosticItem{ID: view.ID, Operation: &view})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}
