package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

const maxDiagnosticRecreationPlans = 16

// The exact recreation adapter retains private native admission behind its finite
// version-matched transport. Pi separately requires fresh destructive consent.
// Neither the request nor the retained native confirmation is client supplied.
// This is the only recreation admission cache; the native executor still owns
// all recreation policy and last-moment deletion/writer/lifetime guards.
type diagnosticRecreationPlan struct {
	request protocol.DiagnosticMaintenanceRequest
	scope   diagnosticRecreationScope
	native  integration.SandboxRecreateRequest
	view    diagnosticRecreationView
	expires time.Time // monotonic; the wall timestamp is disclosure only
}

// All fields are private on purpose. A future transport must explicitly project
// and validate a finite public contract, never marshal the retained plan.
type diagnosticRecreationView struct {
	proposal                            protocol.DiagnosticRecreationPreview
	planID, expiresAt                   string
	feedbackID, scopeID                 string
	revision                            int64
	mode, problem, outcome, operationID string
	resourceRefreshPending              bool
}

type diagnosticRecreationScope struct {
	registration, feedbackID, workspaceID, workspacePath string
	revision                                             int64
}

func copyDiagnosticRecreationView(v diagnosticRecreationView) diagnosticRecreationView {
	if v.proposal.AgentOwnerCount != nil {
		count := *v.proposal.AgentOwnerCount
		v.proposal.AgentOwnerCount = &count
	}
	return v
}

func recreationBindingRequest(req protocol.DiagnosticMaintenanceRequest) protocol.DiagnosticMaintenanceRequest {
	req.Mode, req.PlanID = "", ""
	return req
}

func validDiagnosticRecreationRequest(req protocol.DiagnosticMaintenanceRequest) bool {
	if req.Schema != protocol.DiagnosticsVersion || req.Operation != "recreate" || req.ParentID == "" || len(req.ParentID) > 512 || len(req.Workspace) > 4096 || len(req.FeedbackWorkspace) > 4096 || !filepath.IsAbs(req.FeedbackWorkspace) || filepath.Clean(req.FeedbackWorkspace) != req.FeedbackWorkspace {
		return false
	}
	if req.Scope == "installation" {
		if req.Workspace != "" {
			return false
		}
	} else if (req.Scope != "origin" && req.Scope != "feedback") || !filepath.IsAbs(req.Workspace) || filepath.Clean(req.Workspace) != req.Workspace {
		return false
	}
	return req.Mode == "preview" && req.PlanID == "" || req.Mode == "apply" && hexPlanID(req.PlanID)
}

// Capture exact registered feedback/scope/target identity without a planner or
// runtime call. The native capture additionally binds its full recipe/runtime
// plan. Owner counts and deletion-lease observations are not removal policy.
func (s *Server) diagnosticRecreationScope(ctx context.Context, req protocol.DiagnosticMaintenanceRequest, provider integration.WorkspaceRegistrationDiagnostics) (diagnosticRecreationScope, error) {
	fail := errors.New("exact registered recreation scope unavailable")
	revision := s.store.Revision()
	groups, err := provider.DiagnosticWorkspaceRegistrations()
	if err != nil || len(groups) > 1024 || ctx.Err() != nil {
		return diagnosticRecreationScope{}, fail
	}
	raw, err := json.Marshal(groups)
	if err != nil || len(raw) > 1024*1024 {
		return diagnosticRecreationScope{}, fail
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	var feedback, selected *protocol.DiagnosticWorkspace
	for i := range groups {
		g := &groups[i]
		if !g.Registered || g.ID == "" || len(g.ID) > 512 || !filepath.IsAbs(g.Path) || filepath.Clean(g.Path) != g.Path || len(g.Path) > 4096 || ids[g.ID] || paths[g.Path] {
			return diagnosticRecreationScope{}, fail
		}
		ids[g.ID], paths[g.Path] = true, true
		if g.Path == req.FeedbackWorkspace {
			feedback = g
		}
		if g.ID == req.ParentID {
			selected = g
		}
	}
	if feedback == nil || !feedback.Feedback || selected == nil {
		return diagnosticRecreationScope{}, fail
	}
	if req.Scope != "installation" && (selected.Path != req.Workspace || req.Scope == "feedback" && selected.ID != feedback.ID || req.Scope == "origin" && (feedback.FeedbackOrigin == "" || feedback.FeedbackOrigin != selected.Path)) {
		return diagnosticRecreationScope{}, fail
	}
	if ctx.Err() != nil || s.store.Revision() != revision {
		return diagnosticRecreationScope{}, fail
	}
	return diagnosticRecreationScope{registration: string(raw), feedbackID: feedback.ID, workspaceID: selected.ID, workspacePath: selected.Path, revision: revision}, nil
}

func (s *Server) diagnosticRecreationInputsLive(ctx context.Context, revision int64, expires time.Time) bool {
	// Publication callbacks may themselves cancel or advance revision.
	if s.resourceSnapshot == nil || s.resourceSnapshot().ResourceRefreshPending {
		return false
	}
	return ctx.Err() == nil && s.store.Revision() == revision && (expires.IsZero() || time.Now().Before(expires))
}

func (s *Server) diagnosticRecreation(ctx context.Context, req protocol.DiagnosticMaintenanceRequest) (diagnosticRecreationView, error) {
	fail := func() (diagnosticRecreationView, error) {
		return diagnosticRecreationView{}, errors.New("exact native recreation unavailable or changed; outcome may be unknown, do not replay or automatically retry")
	}
	if !validDiagnosticRecreationRequest(req) || ctx.Err() != nil || s.markResourcesPending == nil || s.resourceSnapshot == nil || !s.maintenanceMu.TryLock() {
		return fail()
	}
	defer s.maintenanceMu.Unlock()
	if s.refreshPlan != nil && s.refreshPlan.view.Status == "running" {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Consume by opaque identity, not a client target lookup, BEFORE every live
	// scope/provider/freshness check. Wrong binding, expiry, lost replies and native
	// errors never restore it. No result replay or silent replacement is offered.
	var retained diagnosticRecreationPlan
	if req.Mode == "apply" {
		found := false
		for id, plan := range s.recreationPlans {
			if plan.view.planID == req.PlanID {
				retained = plan
				delete(s.recreationPlans, id)
				found = true
				break
			}
		}
		if !found || retained.request != recreationBindingRequest(req) || !s.diagnosticRecreationInputsLive(ctx, retained.scope.revision, retained.expires) || retained.view.proposal.Status != "recreate" {
			return fail()
		}
	}
	provider, err := s.integrations.WorkspaceDiagnostics()
	if err != nil {
		return fail()
	}
	native, ok := provider.(integration.SandboxRecreationDiagnostics)
	if !ok {
		return fail()
	}
	before, err := s.diagnosticRecreationScope(ctx, req, provider)
	if err != nil || !s.diagnosticRecreationInputsLive(ctx, before.revision, time.Time{}) {
		return fail()
	}
	if req.Mode == "apply" {
		if before != retained.scope || !s.diagnosticRecreationInputsLive(ctx, before.revision, retained.expires) {
			return fail()
		}
		// No planner call to recover/rewrite a confirmation. The production executor
		// replans to COMPARE its same-capture ExpectedPlanID and retains its guards.
		op := s.store.BeginOperation("workspace", 0, before.workspaceID)
		result, executeErr := native.RecreateSandboxes(ctx, s.logger, retained.native)
		view := copyDiagnosticRecreationView(retained.view)
		view.mode, view.operationID, view.problem, view.outcome = "apply", op, "none", "unverified_may_be_partial"
		completed := 0
		valid := len(result.Targets) == 1 && result.Targets[0].WorkspaceID == before.workspaceID && result.Targets[0].WorkspacePath == before.workspacePath && result.Targets[0].Status == "recreated" && result.OK && !result.Cancelled && result.Recreated == 1 && result.Failed == 0 && result.Skipped == 0
		status := "failed"
		if executeErr == nil && valid && ctx.Err() == nil {
			view.outcome = "native_restoration_checks_returned"
			completed = 1
			status = "succeeded"
		} else if executeErr != nil {
			view.problem = "native_error"
		} else {
			view.problem = "invalid_or_incomplete_native_result"
		}
		if ctx.Err() != nil {
			view.problem = "cancelled_or_timed_out"
		}
		// Even a refusal/error can follow partial metadata/resource effects. Request
		// ordinary resource reconciliation, not collection refresh, GC or a retry.
		s.markResourcesPending()
		view.resourceRefreshPending = true
		s.store.FinishOperation(op, protocol.ManagedOperation{Status: status, CompletedTargets: completed, TotalTargets: 1})
		return view, nil
	}
	proposal, request, err := native.PrepareDiagnosticRecreation(ctx, before.workspaceID, before.workspacePath)
	if err != nil || !protocol.ValidDiagnosticRecreationPreview(proposal, before.workspaceID) || request.Workspace != before.workspacePath || !filepath.IsAbs(request.WorkspaceRoot) || filepath.Clean(request.WorkspaceRoot) != request.WorkspaceRoot || len(request.WorkspaceRoot) > 4096 || !hexPlanID(request.ExpectedPlanID) {
		return fail()
	}
	// Freeze borrowed public pointer fields immediately, before any further
	// registration/publication callback can mutate provider-owned memory.
	proposal = copyDiagnosticRecreationView(diagnosticRecreationView{proposal: proposal}).proposal
	after, err := s.diagnosticRecreationScope(ctx, req, provider)
	if err != nil || after != before || !s.diagnosticRecreationInputsLive(ctx, before.revision, time.Time{}) {
		return fail()
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fail()
	}
	// Final callback-sensitive check before changing any earlier retained plan.
	if !s.diagnosticRecreationInputsLive(ctx, before.revision, time.Time{}) {
		return fail()
	}
	now := time.Now()
	for id, plan := range s.recreationPlans {
		if !now.Before(plan.expires) || plan.scope.revision != before.revision {
			delete(s.recreationPlans, id)
		}
	}
	if _, exists := s.recreationPlans[before.workspaceID]; !exists && len(s.recreationPlans) >= maxDiagnosticRecreationPlans {
		return fail()
	}
	scopeID := ""
	if req.Scope != "installation" {
		scopeID = before.workspaceID
	}
	view := diagnosticRecreationView{proposal: proposal, planID: hex.EncodeToString(token[:]), expiresAt: now.Add(time.Minute).UTC().Format(time.RFC3339Nano), feedbackID: before.feedbackID, scopeID: scopeID, revision: before.revision, mode: "preview", problem: "none", outcome: "not_attempted"}
	if s.recreationPlans == nil {
		s.recreationPlans = map[string]diagnosticRecreationPlan{}
	}
	s.recreationPlans[before.workspaceID] = diagnosticRecreationPlan{request: recreationBindingRequest(req), scope: before, native: request, view: copyDiagnosticRecreationView(view), expires: now.Add(time.Minute)}
	return copyDiagnosticRecreationView(view), nil
}

// Explicit projection only: never marshal the retained request or native result.
func projectDiagnosticRecreation(v diagnosticRecreationView, req protocol.DiagnosticMaintenanceRequest) protocol.DiagnosticRecreation {
	v = copyDiagnosticRecreationView(v)
	return protocol.DiagnosticRecreation{Schema: protocol.DiagnosticsVersion, Operation: "recreate", Mode: v.mode, Scope: req.Scope, FeedbackWorkspaceID: v.feedbackID, ScopeWorkspaceID: v.scopeID, Evidence: "native_exact_workspace_recreation_plan_not_consent", PlanID: v.planID, ExpiresAt: v.expiresAt, Revision: v.revision, Proposal: v.proposal, Problem: v.problem, Outcome: v.outcome, OperationID: v.operationID, ResourceRefreshPending: v.resourceRefreshPending, Limitations: protocol.DiagnosticRecreationLimitations()}
}
