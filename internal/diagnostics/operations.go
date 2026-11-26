package diagnostics

import (
	"encoding/hex"
	"sort"
	"strings"
	"time"

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
			// Agent refs expose their cached workspace relationship in metadata,
			// not SourceRef.WorkspaceID. This joins observations only; it is
			// never fresh ownership or permission to execute the operation.
			if ref.Snapshot.IsDurableAgent() && ref.Snapshot.Metadata["workspace_id"] != "" && recordInWorkspace([]state.SourceRefRecord{ref}, req.Workspace) {
				workspaces[ref.Snapshot.Metadata["workspace_id"]] = true
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
		view := protocol.DiagnosticOperation{ID: clip(op.ID), Kind: "unknown", Status: "unknown", TaskID: op.TaskID, WorkspaceID: clip(op.WorkspaceID), ExecutionEvidence: "unknown_not_captured", TargetEvidence: "unknown_not_captured", CleanupTargetEvidence: "unknown_not_captured", CleanupExecutionEvidence: "unknown_not_captured", TimingEvidence: "unknown_not_captured", PhaseTimingEvidence: "unknown_not_captured"}
		switch op.Kind {
		case "agent-stop", "refresh", "cleanup", "workspace", "task-create", "task-done", "task-reopen", "task-priority", "task-mute", "task-unmute", "task-title", "task-note", "task-delete":
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
		if validOperationTime(op.StartedAt) {
			if view.Status == "running" && op.FinishedAt == "" && op.ElapsedMilliseconds == 0 {
				view.TimingEvidence, view.StartedAt = "native_handler_start_only", op.StartedAt
			} else if (view.Status == "succeeded" || view.Status == "failed" || view.Status == "partial") && validOperationTime(op.FinishedAt) && op.ElapsedMilliseconds >= 0 && op.ElapsedMilliseconds <= int64((1<<63-1)/time.Millisecond) {
				view.TimingEvidence, view.StartedAt, view.FinishedAt = "native_handler_monotonic_elapsed", op.StartedAt, op.FinishedAt
				elapsed := op.ElapsedMilliseconds
				view.ElapsedMilliseconds = &elapsed
			}
		}
		if view.Kind == "cleanup" && (view.Status == "succeeded" || view.Status == "failed" || view.Status == "partial") {
			if op.CleanupExecutionUnavailable || (op.CleanupExecution != nil && !protocol.ValidCleanupExecution(*op.CleanupExecution, op.Status, op.CompletedTargets, op.TotalTargets)) {
				view.CleanupExecutionEvidence = "unavailable_invalid_or_over_budget"
			} else if op.CleanupExecution != nil {
				view.CleanupExecutionEvidence = "native_executor_dispatch_boundaries_not_resource_verification"
				value := *op.CleanupExecution
				view.CleanupExecution = &value
			}
			if op.CleanupTargetsUnavailable || (op.CleanupTargetsCaptured && !validCleanupTargetSummary(op)) {
				view.CleanupTargetEvidence = "unavailable_invalid_or_over_budget"
			} else if op.CleanupTargetsCaptured {
				view.CleanupTargetEvidence = "native_returned_target_prefix_not_resource_verification"
				view.CleanupTargets = append([]protocol.OperationCleanupTarget{}, op.CleanupTargets...)
				// Refine only with validated boundaries captured for this same
				// operation. No Cleanup call is not proof of no preflight effects,
				// and either returned success or error is still unverified state.
				if view.CleanupExecution != nil {
					view.CleanupTargetEvidence = "native_target_returns_with_dispatch_boundaries_not_resource_verification"
					for i := op.CompletedTargets; i < len(view.CleanupTargets); i++ {
						view.CleanupTargets[i].Outcome = "not_dispatched_by_cleanup_executor"
						if view.CleanupExecution.Stage == "provider_call" && i == op.CompletedTargets {
							view.CleanupTargets[i].Outcome = "returned_error_may_be_partial"
						}
					}
				}
				truncated := op.CleanupTargetsTruncated
				view.CleanupTargetsTruncated = &truncated
			}
		}
		if view.Kind == "cleanup" && (view.Status == "running" || view.Status == "succeeded" || view.Status == "failed" || view.Status == "partial") {
			if op.PhaseTimingsUnavailable || !validOperationPhases(op.PhaseTimings) {
				view.PhaseTimingEvidence = "unavailable_invalid_or_overflow"
			} else if len(op.PhaseTimings) > 0 {
				view.PhaseTimingEvidence = "native_completed_phase_aggregates"
				view.PhaseTimings = append([]protocol.OperationPhaseTiming{}, op.PhaseTimings...)
			}
		}
		items = append(items, protocol.DiagnosticItem{ID: view.ID, Operation: &view})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func validOperationTime(value string) bool {
	if len(value) < 20 || len(value) > 35 {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !parsed.IsZero() && parsed.Year() >= 1 && parsed.Year() <= 9999 && parsed.UTC().Format(time.RFC3339Nano) == value
}

func validCleanupTargetSummary(op protocol.ManagedOperation) bool {
	if op.TotalTargets < 1 || op.TotalTargets > 512 || op.CompletedTargets < 0 || op.CompletedTargets > op.TotalTargets ||
		(op.Status == "succeeded" && op.CompletedTargets != op.TotalTargets) ||
		(op.Status == "failed" && op.CompletedTargets != 0) ||
		(op.Status == "partial" && (op.CompletedTargets == 0 || op.CompletedTargets == op.TotalTargets)) ||
		len(op.CleanupTargets) != min(op.TotalTargets, protocol.DiagnosticChildLimit) ||
		op.CleanupTargetsTruncated != (op.TotalTargets > protocol.DiagnosticChildLimit) {
		return false
	}
	seen := map[string]bool{}
	for i, target := range op.CleanupTargets {
		hexID := strings.TrimPrefix(target.ID, "cleanup-result:")
		_, err := hex.DecodeString(hexID)
		outcome := "not_completed_may_be_partial"
		if i < op.CompletedTargets {
			outcome = "returned_success_unverified"
		}
		if target.Index != i || len(target.ID) != 79 || len(hexID) != 64 || err != nil || hexID != strings.ToLower(hexID) || seen[target.ID] || !protocol.ManagedCleanupTargetKind(target.Provider, target.Kind) || target.Outcome != outcome {
			return false
		}
		seen[target.ID] = true
	}
	return true
}

func validOperationPhases(phases []protocol.OperationPhaseTiming) bool {
	if len(phases) > 11 {
		return false
	}
	previous := ""
	for _, phase := range phases {
		if !protocol.ManagedCleanupPhase(phase.Phase) || phase.Phase <= previous || phase.CompletedIntervals < 1 || phase.CompletedIntervals > protocol.MaxOperationPhaseIntervals || phase.ElapsedMilliseconds < 0 || phase.ElapsedMilliseconds > protocol.MaxOperationPhaseMilliseconds {
			return false
		}
		previous = phase.Phase
	}
	return true
}
