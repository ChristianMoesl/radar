package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

// gcPlanItems describes the same cached-input candidate/skip plan used by GC.
// The workspace manager supplies the exact production root. No provider preview,
// path-presence check, lease acquisition or cleanup is performed.
func gcPlanItems(req protocol.DiagnosticsRequest, records []state.TaskRecord, refs []state.SourceRefRecord, now time.Time, providers integration.Registry) ([]protocol.DiagnosticItem, error) {
	byRecord := map[string][]state.SourceRefRecord{}
	for _, ref := range refs {
		byRecord[ref.TaskRecordID] = append(byRecord[ref.TaskRecordID], ref)
	}
	manager, err := providers.WorkspaceManager()
	if err != nil {
		return nil, fmt.Errorf("production GC planner workspace configuration unsupported")
	}
	root, err := manager.DefaultRoot()
	if err != nil || strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("production GC planner workspace configuration unavailable")
	}
	items := []protocol.DiagnosticItem{}
	seen := map[string]*protocol.DiagnosticGCPlan{}
	for _, record := range records {
		if req.ParentID != "" && req.ParentID != record.ID && req.ParentID != strconv.Itoa(record.NumericID) {
			continue
		}
		if req.State != "" && req.State != record.State {
			continue
		}
		refs := byRecord[record.ID]
		if req.Workspace != "" && !recordInWorkspace(refs, req.Workspace) {
			continue
		}
		plan, err := workspacegc.InspectPlan([]state.TaskRecord{record}, refs, now, workspacegc.Options{WorkspaceRoot: root})
		if err != nil {
			return nil, fmt.Errorf("production GC planner unavailable")
		}
		add := func(kind string, view protocol.DiagnosticGCPlan) {
			identity, _ := json.Marshal(struct{ Record, Kind, Path, Workspace string }{record.ID, kind, view.Path, view.WorkspaceID})
			sum := sha256.Sum256(identity)
			view.ID = "gc-plan:" + hex.EncodeToString(sum[:])
			if req.ID != "" && req.ID != view.ID {
				return
			}
			if existing := seen[view.ID]; existing != nil {
				existing.Occurrences++
				return
			}
			view.RecordID, view.TaskID = clip(record.ID), record.NumericID
			view.Path, view.WorkspaceID = clip(view.Path), clip(view.WorkspaceID)
			view.GCRetention = gcRetentionView(record, refs, now)
			view.Evidence = "production_gc_plan_on_cached_inputs"
			view.ExecutionEligibility = "unknown_provider_ownership_lease_checks_not_run"
			view.Occurrences = 1
			seen[view.ID] = &view
			items = append(items, protocol.DiagnosticItem{ID: view.ID, GCPlan: &view})
		}
		for _, candidate := range plan.Candidates {
			view := protocol.DiagnosticGCPlan{Decision: "candidate", Path: candidate.Path, WorkspaceID: candidate.WorkspaceID}
			mode, err := workspacegc.CandidateMode(candidate, now)
			if err != nil {
				view.Decision = "completion_time_missing_or_invalid"
			} else {
				switch mode {
				case integration.CleanupSafe:
					view.NextStage = "safe_cleanup"
				case integration.CleanupRetire:
					view.NextStage = "runtime_retirement"
				case integration.CleanupExpired:
					view.NextStage = "expired_cleanup"
				}
			}
			if expiry, ok := workspacegc.ExpiryAt(candidate.Task); ok && candidate.WorkspaceID != "" {
				view.ExpiryAt = expiry.UTC().Format(time.RFC3339Nano)
			}
			add("resource", view)
		}
		for _, skipped := range plan.Skipped {
			kind := "resource"
			if skipped.Path == "" {
				kind = "task_gate"
			}
			add(kind, protocol.DiagnosticGCPlan{Decision: skipped.Decision, Path: skipped.Path, WorkspaceID: skipped.WorkspaceID})
		}
		if len(plan.Candidates) == 0 && len(plan.Skipped) == 0 {
			decision := workspacegc.InspectRetention(record, refs, now).Decision
			if decision == "retention_gate_passed" {
				decision = "no_workspace_candidates"
			}
			add("task_gate", protocol.DiagnosticGCPlan{Decision: decision})
		}
	}
	return items, nil
}
