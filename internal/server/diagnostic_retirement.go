package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/phasetiming"
	"radar/internal/protocol"
	"radar/internal/workspacegc"
)

// Attached to the existing bounded per-task nativeCleanupPlan, not a second
// cache. Native private fields and closures never enter transport or JSON.
type diagnosticRetirementBinding struct {
	Request      protocol.DiagnosticMaintenanceRequest
	Registration string // exact registered identities, including private runtime names
	View         protocol.DiagnosticRetirement
}
type diagnosticRetirementPlan struct {
	native       workspacegc.RetirementPlan
	binding      diagnosticRetirementBinding
	scope        diagnosticRecreationScope
	admission    *diagnosticRetirementAdmission
	privateBytes int // Native candidate/task bytes also count toward the existing plan bound.
}
type diagnosticRetirementAdmission struct {
	operation string
	expires   time.Time
}

// The daemon supplies taskservice.GuardCleanup, never ordinary refresh or GC.
// Final publication, exact scope and candidate/provider checks stay inside it.
func (s *Server) SetDiagnosticRetirementGuard(guard func(context.Context, protocol.Task, func() error) error) *Server {
	s.retirementGuard = guard
	return s
}

func validDiagnosticRetirementRequest(q protocol.DiagnosticMaintenanceRequest) bool {
	if q.Operation != "retire" {
		return false
	}
	q.Operation = "recreate"
	return validDiagnosticRecreationRequest(q)
}
func (s *Server) diagnosticRetirement(ctx context.Context, q protocol.DiagnosticMaintenanceRequest) (protocol.DiagnosticRetirement, error) {
	fail := func() (protocol.DiagnosticRetirement, error) {
		return protocol.DiagnosticRetirement{}, errors.New("exact native retirement unavailable or changed; outcome may be unknown, do not replay or automatically retry")
	}
	if !validDiagnosticRetirementRequest(q) || ctx.Err() != nil || s.retirementGuard == nil || s.markResourcesPending == nil || s.resourceSnapshot == nil || !s.maintenanceMu.TryLock() {
		return fail()
	}
	defer s.maintenanceMu.Unlock()
	if s.refreshPlan != nil && s.refreshPlan.view.Status == "running" {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var retained diagnosticRetirementPlan
	var expires time.Time
	if q.Mode == "apply" {
		s.cleanupPlanMu.Lock()
		found := false
		for id, p := range s.cleanupPlans {
			if p.id == q.PlanID {
				delete(s.cleanupPlans, id)
				if p.retirement != nil && len(p.diagnostic) > 0 && time.Now().Before(p.expires) && p.revision == s.store.Revision() && json.Unmarshal(p.diagnostic, &retained.binding) == nil && retained.binding.Request == cleanupBindingRequest(q) {
					retained.native = p.retirement.native
					retained.scope = p.retirement.scope
					retained.admission = p.retirement.admission
					expires = p.expires
					found = true
				}
				break
			}
		}
		s.cleanupPlanMu.Unlock()
		if !found || !s.diagnosticCleanupInputsLive(ctx, expires, retained.scope.revision) {
			return fail()
		}
	}
	provider, err := s.integrations.WorkspaceDiagnostics()
	if err != nil {
		return fail()
	}
	before, err := s.diagnosticRecreationScope(ctx, q, provider)
	if err != nil || !s.diagnosticRecreationInputsLive(ctx, before.revision, time.Time{}) {
		return fail()
	}
	if q.Mode == "apply" {
		saved := retained.scope
		if saved != before || retained.binding.Registration != before.registration || !s.diagnosticCleanupInputsLive(ctx, expires, before.revision) {
			return fail()
		}
		preview := retained.native.Preview()
		view := retained.binding.View
		if !protocol.ValidDiagnosticRetirement(view, protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "retire", Mode: "preview", Scope: q.Scope, ParentID: q.ParentID}) {
			return fail()
		}
		// The retained original callback performs scope/publication checks and fresh
		// authored lifecycle admission. This wrapper starts managed evidence only
		// inside it, after admission, immediately before native candidate execution.
		native := retained.native
		result, _, executeErr := native.Execute(phasetiming.WithLogger(ctx, s.logger), s.store, s.cleanupService)
		op := retained.admission.operation
		if op == "" {
			return fail()
		}
		status := "succeeded"
		view.Mode, view.OperationID, view.Problem, view.Outcome = "apply", op, "none", "native_bundle_returned_unverified"
		if executeErr != nil {
			status = "failed"
			if len(result.Targets) > 0 {
				status = "partial"
			}
			view.Problem, view.Outcome = "native_error", "unverified_may_be_partial"
		}
		if ctx.Err() != nil {
			view.Problem, view.Outcome = "cancelled_or_timed_out", "unverified_may_be_partial"
			status = "failed"
			if len(result.Targets) > 0 {
				status = "partial"
			}
		}
		completed := len(result.Targets)
		view.CompletedTargets = &completed
		if result.ExecutionObservation != nil && protocol.ValidCleanupExecution(*result.ExecutionObservation, status, completed, len(preview.Targets)) {
			copy := *result.ExecutionObservation
			view.Execution = &copy
		}
		s.markResourcesPending()
		view.ResourceRefreshPending = true
		s.store.FinishCleanupOperation(op, protocol.ManagedOperation{Status: status, TaskID: preview.TaskID, WorkspaceID: before.workspaceID, CompletedTargets: completed, TotalTargets: len(preview.Targets)}, preview.Targets, result.ExecutionObservation)
		if !protocol.ValidDiagnosticRetirement(view, q) {
			return fail()
		}
		return view, nil
	}
	manager, err := s.integrations.WorkspaceManager()
	if err != nil {
		return fail()
	}
	root, err := manager.DefaultRoot()
	if err != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fail()
	}
	now := time.Now()
	plan, err := workspacegc.BuildPlan(s.store, now, workspacegc.Options{WorkspaceRoot: root})
	if err != nil {
		return fail()
	}
	var candidate workspacegc.Candidate
	matches := 0
	for _, c := range plan.Candidates {
		if c.WorkspaceID == before.workspaceID {
			candidate = c
			matches++
		}
	}
	if matches != 1 || candidate.Path != before.workspacePath {
		return fail()
	}
	nativePayload, e := json.Marshal(candidate)
	if e != nil || len(nativePayload) > maxCleanupPlanBytes {
		return fail()
	}
	providers := s.integrations.CleanupProviders()
	seen := map[string]bool{}
	if len(providers) < 1 || len(providers) > 4 {
		return fail()
	}
	for _, p := range providers {
		name := p.Descriptor().Name
		if seen[name] || (name != "git" && name != "sbx" && name != "tmux" && name != "workspace") {
			return fail()
		}
		seen[name] = true
	}
	guard := s.retirementGuard
	admission := &diagnosticRetirementAdmission{}
	options := workspacegc.Options{WorkspaceRoot: root}
	// Captures only daemon-owned scope/options. No future user request can replace
	// the policy/time/guard or selected native targets. Preparation never calls it.
	options.GuardExecution = func(c context.Context, task protocol.Task, execute func() error) error {
		return guard(c, task, func() error {
			fresh, e := s.diagnosticRecreationScope(c, q, provider)
			if e != nil || fresh != before || !s.diagnosticRecreationInputsLive(c, before.revision, admission.expires) {
				return errors.New("retirement scope changed before execution")
			}
			admission.operation = s.store.BeginOperation("gc", task.ID, before.workspaceID)
			return execute()
		})
	}
	native, err := workspacegc.PrepareRetirement(ctx, s.store, s.cleanupService, candidate, now, options)
	if err != nil {
		return fail()
	}
	preview := native.Preview()
	proposal, registration, err := s.discloseDiagnosticRetirement(ctx, preview, candidate, before)
	if err != nil {
		return fail()
	}
	after, err := s.diagnosticRecreationScope(ctx, q, provider)
	if err != nil || after != before || !s.diagnosticRecreationInputsLive(ctx, before.revision, time.Time{}) {
		return fail()
	}
	view := protocol.DiagnosticRetirement{Schema: protocol.DiagnosticsVersion, Operation: "retire", Mode: "preview", Scope: q.Scope, FeedbackWorkspaceID: before.feedbackID, Evidence: "native_exact_workspace_retirement_plan_not_consent", Revision: before.revision, Proposal: proposal, Problem: "none", Outcome: "not_attempted", Limitations: protocol.DiagnosticRetirementLimitations()}
	if q.Scope != "installation" {
		view.ScopeWorkspaceID = before.workspaceID
	}
	binding := diagnosticRetirementBinding{Request: cleanupBindingRequest(q), Registration: registration, View: view}
	issued, err := s.issueBoundNativeCleanupPlan(ctx, preview, before.revision, nil, &diagnosticRetirementPlan{native: native, binding: binding, scope: before, admission: admission, privateBytes: len(nativePayload)})
	if err != nil {
		return fail()
	}
	s.cleanupPlanMu.Lock()
	p, ok := s.cleanupPlans[issued.TaskID]
	if ok && p.id == issued.PlanID {
		err = json.Unmarshal(p.diagnostic, &binding)
	} else {
		err = errors.New("superseded")
	}
	s.cleanupPlanMu.Unlock()
	if err != nil || !protocol.ValidDiagnosticRetirement(binding.View, q) {
		return fail()
	}
	return binding.View, nil
}

func (s *Server) discloseDiagnosticRetirement(ctx context.Context, preview protocol.CleanupPreview, c workspacegc.Candidate, scope diagnosticRecreationScope) (protocol.DiagnosticRetirementPreview, string, error) {
	fail := errors.New("complete native retirement disclosure unavailable")
	var groups []protocol.DiagnosticWorkspace
	if json.Unmarshal([]byte(scope.registration), &groups) != nil {
		return protocol.DiagnosticRetirementPreview{}, "", fail
	}
	var selected protocol.DiagnosticWorkspace
	for _, g := range groups {
		if g.ID == scope.workspaceID {
			selected = g
		}
	}
	owners, fingerprint, err := agentservice.DiagnosticWorkspaceRemovalOwners(ctx, []string{scope.workspacePath})
	ownerCount, ownerPresent := owners[scope.workspacePath]
	if err != nil || !ownerPresent || ownerCount != 0 {
		return protocol.DiagnosticRetirementPreview{}, "", fail
	}
	count := 0
	view := protocol.DiagnosticRetirementPreview{ID: c.WorkspaceID, RecordID: c.RecordID, TaskID: c.TaskID, Stage: "runtime_retirement", Targets: []protocol.DiagnosticRetirementTarget{}, Preserved: []string{"workspace_files", "worktrees_and_branches", "sandbox_and_local_state", "registration"}, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "fresh_bounded_registration", AgentOwnerCount: &count, LeaseEvidence: "unknown_not_checked"}
	if len(preview.Targets) < 1 || len(preview.Targets) > 3 {
		return view, "", fail
	}
	seen := map[string]bool{}
	for i, t := range preview.Targets {
		if t.WorkspaceID != c.WorkspaceID || t.Path != c.Path || seen[t.Source] || len(t.Safety) > 1024 || len(cleanup.AutomaticBlockingMessages([]protocol.CleanupTarget{t}, false)) > 0 {
			return view, "", fail
		}
		seen[t.Source] = true
		expected, kind, effect := selected.ID, "workspace", "retain_workspace_files_and_registration"
		switch t.Source {
		case "workspace":
			if i != len(preview.Targets)-1 {
				return view, "", fail
			}
		case "tmux":
			expected, kind, effect = selected.SessionName, "session", "remove_terminal_session_and_processes"
		case "sbx":
			expected, kind, effect = selected.SandboxName, "sandbox", "stop_sandbox_commands_and_services"
		default:
			return view, "", fail
		}
		if expected == "" || t.ResourceID != expected || t.Kind != kind || t.ProvidesWorkspace != (t.Source == "workspace") {
			return view, "", fail
		}
		raw, e := json.Marshal(t)
		if e != nil || len(raw) > 64*1024 {
			return view, "", fail
		}
		hash := sha256.Sum256(append([]byte{byte(i)}, raw...))
		view.Targets = append(view.Targets, protocol.DiagnosticRetirementTarget{ID: "retirement-target:" + hex.EncodeToString(hash[:]), Provider: t.Source, Effect: effect, SafetyNoticeCount: len(t.Safety)})
	}
	if !seen["workspace"] || seen["tmux"] != (selected.SessionName != "") || seen["sbx"] != (selected.SandboxName != "") {
		return view, "", fail
	}
	fresh, stamp, e := agentservice.DiagnosticWorkspaceRemovalOwners(ctx, []string{scope.workspacePath})
	if e != nil || stamp != fingerprint || !reflect.DeepEqual(fresh, owners) || ctx.Err() != nil {
		return view, "", fail
	}
	return view, scope.registration, nil
}
