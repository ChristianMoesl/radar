package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"radar/internal/diagnostics"
	"radar/internal/protocol"
)

// Attached to nativeCleanupPlan, not an independent cache/executor. All fields
// are serialized before retention; no provider/client maps or slices are held.
type diagnosticCleanupContext struct {
	Request protocol.DiagnosticMaintenanceRequest
	Scope   diagnostics.ManualCleanupScope
	View    protocol.DiagnosticCleanup
}

func cleanupBindingRequest(req protocol.DiagnosticMaintenanceRequest) protocol.DiagnosticMaintenanceRequest {
	req.Mode, req.PlanID = "", ""
	return req
}

func validDiagnosticCleanupRequest(req protocol.DiagnosticMaintenanceRequest) bool {
	return req.Schema == protocol.DiagnosticsVersion && req.Operation == "cleanup" && (req.Mode == "preview" && req.PlanID == "" || req.Mode == "apply" && hexPlanID(req.PlanID))
}

func (s *Server) diagnosticCleanup(ctx context.Context, req protocol.DiagnosticMaintenanceRequest) (protocol.DiagnosticCleanup, error) {
	fail := func() (protocol.DiagnosticCleanup, error) {
		return protocol.DiagnosticCleanup{}, errors.New("exact native manual cleanup unavailable or plan changed; do not replay or automatically retry")
	}
	if !validDiagnosticCleanupRequest(req) || ctx.Err() != nil || s.resourceSnapshot == nil || !s.maintenanceMu.TryLock() {
		return fail()
	}
	defer s.maintenanceMu.Unlock()
	if s.refreshPlan != nil && s.refreshPlan.view.Status == "running" {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if req.Mode == "apply" {
		// Consume before live validation. A wrong scope, lost response, changed
		// registration, expiry or native failure cannot make this identity reusable.
		preview, binding, expires, err := s.takeDiagnosticCleanupPlan(req)
		if err != nil {
			return fail()
		}
		current, err := diagnostics.CaptureManualCleanupScope(ctx, s.store, req, s.integrations)
		if err != nil || current != binding.Scope || !s.diagnosticCleanupInputsLive(ctx, expires, binding.Scope.Revision) {
			return fail()
		}
		if task, ok := uniqueCleanupTask(s.filteredTasks(), preview.TaskID); !ok || !diagnostics.ManualCleanupTaskInScope(task, req.Workspace) {
			return fail()
		}
		// Filters/registration callbacks can change inputs or outlive cooperative
		// deadlines. Recheck after them, without reserving future native safety.
		current, err = diagnostics.CaptureManualCleanupScope(ctx, s.store, req, s.integrations)
		if err != nil || current != binding.Scope || !s.diagnosticCleanupInputsLive(ctx, expires, binding.Scope.Revision) {
			return fail()
		}
		result, executeErr := s.executeCleanup(ctx, preview)
		view := binding.View
		view.Mode, view.Status, view.OperationID = "apply", "returned", result.OperationID
		view.ResourceRefreshPending = result.ResourceRefreshPending
		view.Problem = "none"
		status := "succeeded"
		if executeErr != nil {
			view.Problem, status = "native_error", "failed"
			if len(result.Targets) > 0 {
				status = "partial"
			}
		}
		if ctx.Err() != nil {
			view.Problem = "cancelled_or_timed_out"
		}
		completed := len(result.Targets)
		view.CompletedTargets = &completed
		if result.ExecutionObservation != nil && protocol.ValidCleanupExecution(*result.ExecutionObservation, status, completed, len(preview.Targets)) {
			copy := *result.ExecutionObservation
			view.Execution = &copy
		}
		return view, nil
	}
	if s.resourceSnapshot().ResourceRefreshPending {
		return fail()
	}
	before, err := diagnostics.CaptureManualCleanupScope(ctx, s.store, req, s.integrations)
	if err != nil {
		return fail()
	}
	// The production service and its ordered complete provider bundle are shared
	// with CLI/TUI. Unknown/ambiguous native effects cannot be safely disclosed.
	providers := s.integrations.CleanupProviders()
	seen := map[string]bool{}
	if len(providers) == 0 || len(providers) > 4 {
		return fail()
	}
	for _, p := range providers {
		name := p.Descriptor().Name
		if seen[name] || (name != "git" && name != "sbx" && name != "tmux" && name != "workspace") {
			return fail()
		}
		seen[name] = true
	}
	preview, revision, err := s.prepareCleanupPreview(ctx, before.TaskID, &req)
	if err != nil || revision != before.Revision {
		return fail()
	}
	// Freeze immediately after the shared planner, before ownership/lease or
	// registration callbacks. Later provider-owned map changes must not silently
	// replace the private operations that the complete disclosure represents.
	nativeRaw, err := json.Marshal(preview)
	if err != nil || len(nativeRaw) > maxCleanupPlanBytes || len(preview.Targets) < 1 || len(preview.Targets) > 512 {
		return fail()
	}
	var frozen protocol.CleanupPreview
	if json.Unmarshal(nativeRaw, &frozen) != nil {
		return fail()
	}
	preview = frozen
	for _, target := range preview.Targets {
		if !seen[target.Source] {
			return fail()
		}
	}
	targets, err := diagnostics.DiscloseManualCleanup(ctx, preview, before)
	if err != nil {
		return fail()
	}
	after, err := diagnostics.CaptureManualCleanupScope(ctx, s.store, req, s.integrations)
	if err != nil || after != before || ctx.Err() != nil || s.resourceSnapshot().ResourceRefreshPending {
		return fail()
	}
	view := protocol.DiagnosticCleanup{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: "preview", Scope: req.Scope, RecordID: before.RecordID, TaskID: before.TaskID, FeedbackWorkspaceID: before.FeedbackID, ScopeWorkspaceID: before.WorkspaceID, Evidence: "native_exact_manual_cleanup_plan_not_consent", Status: "preview", Problem: "none", Revision: before.Revision, Targets: targets, Limitations: protocol.DiagnosticCleanupLimitations()}
	// Leave room for plan ID/expiry and the fixed envelope, never truncate.
	raw, err := json.Marshal(view)
	if err != nil || len(raw) > 30*1024 {
		return fail()
	}
	binding := diagnosticCleanupContext{Request: cleanupBindingRequest(req), Scope: before, View: view}
	issued, err := s.issueBoundCleanupPlan(ctx, preview, revision, &binding)
	if err != nil {
		return fail()
	}
	// Return the independently decoded retained disclosure, not borrowed slices.
	s.cleanupPlanMu.Lock()
	plan, ok := s.cleanupPlans[issued.TaskID]
	if ok && plan.id == issued.PlanID {
		err = json.Unmarshal(plan.diagnostic, &binding)
	} else {
		err = errors.New("superseded")
	}
	s.cleanupPlanMu.Unlock()
	if err != nil {
		return fail()
	}
	return binding.View, nil
}

// Publication readers can themselves observe cancellation or revision drift.
// Check the live context/revision after that callback, not only before it.
func (s *Server) diagnosticCleanupInputsLive(ctx context.Context, expires time.Time, revision int64) bool {
	if s.resourceSnapshot == nil || s.resourceSnapshot().ResourceRefreshPending {
		return false
	}
	return ctx.Err() == nil && time.Now().Before(expires) && s.store.Revision() == revision
}

func (s *Server) takeDiagnosticCleanupPlan(req protocol.DiagnosticMaintenanceRequest) (protocol.CleanupPreview, diagnosticCleanupContext, time.Time, error) {
	fail := errors.New("native diagnostic cleanup plan unavailable")
	s.cleanupPlanMu.Lock()
	defer s.cleanupPlanMu.Unlock()
	for taskID, plan := range s.cleanupPlans {
		if plan.id != req.PlanID {
			continue
		}
		delete(s.cleanupPlans, taskID)
		var binding diagnosticCleanupContext
		var preview protocol.CleanupPreview
		if len(plan.diagnostic) == 0 || !time.Now().Before(plan.expires) || plan.revision != s.store.Revision() || json.Unmarshal(plan.diagnostic, &binding) != nil || json.Unmarshal(plan.payload, &preview) != nil || binding.Request != cleanupBindingRequest(req) || preview.PlanID != req.PlanID || preview.TaskID != taskID || binding.Scope.TaskID != taskID {
			return protocol.CleanupPreview{}, diagnosticCleanupContext{}, time.Time{}, fail
		}
		return preview, binding, plan.expires, nil
	}
	return protocol.CleanupPreview{}, diagnosticCleanupContext{}, time.Time{}, fail
}
