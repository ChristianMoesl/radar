package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"radar/internal/diagnostics"
	"radar/internal/integration"
	"radar/internal/protocol"
)

// Private native admission behind the strict finite daemon/client projection.
// No CLI/Pi removal or consent is enabled here. Trusted callers must separately
// obtain fresh exact-target registration-removal consent.
// All fields are private: accidentally marshaling a plan or view yields {}.
type diagnosticAgentRemoveView struct {
	schedules, enabledSchedules                                        int
	agent, record, feedback, workspace, planID, expiresAt, operationID string
	taskID                                                             int
	revision                                                           int64
	dispatched                                                         bool
	mode, problem, outcome                                             string
	observation                                                        protocol.DiagnosticAgentRemoveObservation
}
type diagnosticAgentRemovePlan struct {
	request                     protocol.DiagnosticMaintenanceRequest
	scope                       diagnosticAgentScope
	operation                   string
	native                      integration.AgentRemovePlan
	view                        diagnosticAgentRemoveView
	expires                     time.Time
	active, entered, dispatched atomic.Bool
}

func diagnosticAgentRemoveUnavailable() error {
	return errors.New("exact native agent removal unavailable or changed; outcome may be partial or unknown, do not replay or automatically retry")
}
func validDiagnosticAgentRemoveRequest(q protocol.DiagnosticMaintenanceRequest, id string) bool {
	if q.Operation != "agent_remove" || q.AgentID != id || !diagnosticAgentControlID.MatchString(id) {
		return false
	}
	q.Operation, q.AgentID = "recreate", ""
	return validDiagnosticRecreationRequest(q)
}

// Preparation retains one native capture with its original final guard in the
// existing per-task cleanupPlans map. It does not execute admission or persist
// anything. Removal, stop and cleanup supersede each other; there is no second cache.
func (s *Server) prepareDiagnosticAgentRemove(ctx context.Context, q protocol.DiagnosticMaintenanceRequest, id string) (diagnosticAgentRemoveView, error) {
	fail := func() (diagnosticAgentRemoveView, error) {
		return diagnosticAgentRemoveView{}, diagnosticAgentRemoveUnavailable()
	}
	if !validDiagnosticAgentRemoveRequest(q, id) || q.Mode != "preview" || ctx.Err() != nil || s.resourceSnapshot == nil || s.markResourcesPending == nil || !s.maintenanceMu.TryLock() {
		return fail()
	}
	defer s.maintenanceMu.Unlock()
	if s.refreshPlan != nil && s.refreshPlan.view.Status == "running" {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	provider, err := s.integrations.AgentRemoveDiagnostics()
	if err != nil {
		return fail()
	}
	before, err := s.captureDiagnosticAgentScope(ctx, q, id)
	if err != nil || !s.diagnosticRecreationInputsLive(ctx, before.cleanup.Revision, time.Time{}) {
		return fail()
	}
	p := &diagnosticAgentRemovePlan{request: cleanupBindingRequest(q), scope: before}
	// No replaceable apply-time callback. This guard checks exact daemon scope
	// and publication AFTER native revalidation, immediately before effects. It
	// may run only synchronously inside this adapter's serialized consumed apply.
	guard := func(c context.Context, execute func() error) error {
		if !p.active.Load() || !p.entered.CompareAndSwap(false, true) {
			return diagnosticAgentRemoveUnavailable()
		}
		current, err := s.captureDiagnosticAgentScope(c, q, id)
		if err != nil || current != before || !s.diagnosticRecreationInputsLive(c, before.cleanup.Revision, p.expires) || !p.active.Load() {
			return diagnosticAgentRemoveUnavailable()
		}
		p.operation = s.store.BeginOperation("agent-remove", before.cleanup.TaskID, before.workspace.ID)
		p.dispatched.Store(true)
		return execute()
	}
	native, err := provider.PrepareDiagnosticAgentRemove(ctx, id, before.workspace, guard)
	if err != nil || native == nil {
		return fail()
	}
	p.native = native
	preview, expires, nativeBytes := native.Preview(), native.ExpiresAt(), native.RetainedBytes()
	p.expires = expires
	if preview.ID != id || preview.Schedules < 0 || preview.Schedules > protocol.AgentMaxFrameBytes || preview.EnabledSchedules < 0 || preview.EnabledSchedules > preview.Schedules || nativeBytes < 1 || nativeBytes > maxCleanupPlanBytes || expires.IsZero() || expires.After(time.Now().Add(30*time.Second)) {
		return fail()
	}
	after, err := s.captureDiagnosticAgentScope(ctx, q, id)
	if err != nil || after != before || !s.diagnosticRecreationInputsLive(ctx, before.cleanup.Revision, expires) {
		return fail()
	}
	// Scope/request strings also live in the retained closure. Count their full
	// serialized values with ample allocation/closure overhead, not just the
	// tiny public facts or the private plan's deliberately empty JSON object.
	private, err := json.Marshal(struct {
		Request   protocol.DiagnosticMaintenanceRequest
		Scope     diagnostics.ManualCleanupScope
		Workspace protocol.AgentWorkspaceBinding
	}{p.request, before.cleanup, before.workspace})
	if err != nil || len(private) > (maxCleanupPlanBytes-64*1024-nativeBytes)/4 {
		return fail()
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fail()
	}
	p.view = diagnosticAgentRemoveView{agent: id, record: before.cleanup.RecordID, taskID: before.cleanup.TaskID, feedback: before.cleanup.FeedbackID, workspace: before.workspace.ID, revision: before.cleanup.Revision, schedules: preview.Schedules, enabledSchedules: preview.EnabledSchedules, planID: hex.EncodeToString(token[:]), expiresAt: expires.UTC().Format(time.RFC3339Nano), mode: "preview", problem: "none", outcome: "native_exact_remove_plan_not_consent"}
	err = s.retainCleanupPlan(ctx, p.view.taskID, p.view.revision, func(now time.Time) (nativeCleanupPlan, error) {
		if !now.Before(expires) {
			return nativeCleanupPlan{}, diagnosticAgentRemoveUnavailable()
		}
		return nativeCleanupPlan{id: p.view.planID, revision: p.view.revision, expires: expires, agentRemove: p}, nil
	})
	if err != nil {
		return fail()
	}
	return p.view, nil
}

// Trusted callers must enforce fresh exact registration-removal consent separately.
// The finite daemon/client projection does not enable CLI/Pi removal controls.
// Consumption precedes scope/cancellation/expiry checks. Lost replies and all
// failures leave no reusable plan; no rollback or safe retry is implied.
func (s *Server) applyDiagnosticAgentRemove(ctx context.Context, q protocol.DiagnosticMaintenanceRequest, id string) (diagnosticAgentRemoveView, error) {
	fail := func() (diagnosticAgentRemoveView, error) {
		return diagnosticAgentRemoveView{}, diagnosticAgentRemoveUnavailable()
	}
	if !hexPlanID(q.PlanID) || !s.maintenanceMu.TryLock() {
		return fail()
	}
	defer s.maintenanceMu.Unlock()
	var retained nativeCleanupPlan
	s.cleanupPlanMu.Lock()
	for taskID, p := range s.cleanupPlans {
		if p.id == q.PlanID {
			delete(s.cleanupPlans, taskID)
			retained = p
			break
		}
	}
	s.cleanupPlanMu.Unlock()
	p := retained.agentRemove
	if p == nil || !validDiagnosticAgentRemoveRequest(q, id) || q.Mode != "apply" || p.request != cleanupBindingRequest(q) || p.view.agent != id || ctx.Err() != nil || s.markResourcesPending == nil || s.resourceSnapshot == nil || !s.diagnosticCleanupInputsLive(ctx, retained.expires, retained.revision) || (s.refreshPlan != nil && s.refreshPlan.view.Status == "running") {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p.active.Store(true)
	defer p.active.Store(false)
	started := time.Now()
	err := p.native.Execute(ctx)
	view := p.view
	view.mode, view.dispatched, view.operationID = "apply", p.dispatched.Load(), p.operation
	view.observation = protocol.UnknownDiagnosticAgentRemoveObservation()
	if !view.dispatched {
		return fail()
	}
	// Schedule pause may precede a failed registration unlink. Even a failed or
	// cancelled native return invalidates cached resources once dispatch began.
	s.markResourcesPending()
	view.outcome = "native_handler_returned_unverified"
	if err != nil || ctx.Err() != nil {
		view.problem, view.outcome = "native_error", "unverified_may_be_partial"
		if ctx.Err() != nil {
			view.problem = "cancelled_or_timed_out"
		}
	}
	if ctx.Err() == nil {
		view.observation = projectAgentRemoveObservation(p.native.Observation(), started, p.expires)
	}
	if ctx.Err() != nil {
		view.problem, view.outcome = "cancelled_or_timed_out", "unverified_may_be_partial"
		view.observation = protocol.UnknownDiagnosticAgentRemoveObservation()
	}
	status := "succeeded"
	if view.problem != "none" {
		status = "failed"
	}
	s.store.FinishOperation(view.operationID, protocol.ManagedOperation{Status: status, TaskID: view.taskID, WorkspaceID: view.workspace})
	return view, nil
}

// Project only fixed facts. Private plan/registration/payload and approval never
// cross the wire; the original native capture and single executor remain private.
func (s *Server) diagnosticAgentRemove(ctx context.Context, q protocol.DiagnosticMaintenanceRequest) (protocol.DiagnosticAgentRemove, error) {
	var private diagnosticAgentRemoveView
	var err error
	if q.Mode == "preview" {
		private, err = s.prepareDiagnosticAgentRemove(ctx, q, q.AgentID)
	} else {
		private, err = s.applyDiagnosticAgentRemove(ctx, q, q.AgentID)
	}
	if err != nil {
		return protocol.DiagnosticAgentRemove{}, diagnosticAgentRemoveUnavailable()
	}
	view := protocol.DiagnosticAgentRemove{Schema: protocol.DiagnosticsVersion, Operation: "agent_remove", Mode: private.mode, Scope: q.Scope, RecordID: private.record, TaskID: private.taskID, AgentID: private.agent, FeedbackWorkspaceID: private.feedback, AgentWorkspaceID: private.workspace, Evidence: "native_exact_agent_removal_plan_not_consent", PlanID: private.planID, ExpiresAt: private.expiresAt, Revision: private.revision, SchedulesBefore: private.schedules, EnabledSchedulesBefore: private.enabledSchedules, DispatchEvidence: "not_dispatched", Problem: private.problem, Outcome: "not_attempted", OperationID: private.operationID, ResourceRefreshPending: private.dispatched, Limitations: protocol.DiagnosticAgentRemoveLimitations()}
	view.Observation = protocol.UnknownDiagnosticAgentRemoveObservation()
	if q.Scope != "installation" {
		view.ScopeWorkspaceID = private.workspace
	}
	if private.dispatched {
		view.DispatchEvidence, view.Outcome = "native_remove_callback_entered_not_resource_outcome", private.outcome
		view.Observation = private.observation
	}
	if !protocol.ValidDiagnosticAgentRemove(view, q) {
		if q.Mode == "preview" {
			s.discardCleanupPlan(&protocol.CleanupPreview{TaskID: private.taskID, PlanID: private.planID})
		}
		return protocol.DiagnosticAgentRemove{}, diagnosticAgentRemoveUnavailable()
	}
	return view, nil
}
