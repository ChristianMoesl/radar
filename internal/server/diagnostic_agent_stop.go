package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sync/atomic"
	"time"

	"radar/internal/diagnostics"
	"radar/internal/integration"
	"radar/internal/protocol"
)

// Native private plan and view behind the finite daemon/client projection.
// Public CLI/Pi controls still require separate fresh interruption approval.
// All fields are private: accidentally marshaling a plan or view yields {}.
type diagnosticAgentStopScope struct {
	cleanup   diagnostics.ManualCleanupScope
	workspace protocol.AgentWorkspaceBinding
	ref       [32]byte
}
type diagnosticAgentStopView struct {
	agent, record, feedback, workspace, planID, expiresAt, operationID string
	taskID                                                             int
	revision                                                           int64
	desired, running, dispatched                                       bool
	mode, problem, outcome                                             string
}
type diagnosticAgentStopPlan struct {
	request                     protocol.DiagnosticMaintenanceRequest
	scope                       diagnosticAgentStopScope
	operation                   string
	native                      integration.AgentStopPlan
	view                        diagnosticAgentStopView
	expires                     time.Time
	active, entered, dispatched atomic.Bool
}

var diagnosticStopAgentID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func diagnosticAgentStopUnavailable() error {
	return errors.New("exact native agent stop unavailable or changed; outcome may be unknown, do not replay or automatically retry")
}
func validDiagnosticAgentStopRequest(q protocol.DiagnosticMaintenanceRequest, id string) bool {
	if q.Operation != "agent_stop" || q.AgentID != id || !diagnosticStopAgentID.MatchString(id) {
		return false
	}
	q.Operation, q.AgentID = "recreate", ""
	return validDiagnosticRecreationRequest(q)
}

// Bind the exact current production task/ref and the complete registration
// observation. A task's unrelated in-scope ref cannot authorize a different
// agent. No native runner, checkpoint, token, lease or workspace preparation is
// read here; PrepareDiagnosticAgentStop owns native identity/runtime capture.
func (s *Server) captureDiagnosticAgentStopScope(ctx context.Context, q protocol.DiagnosticMaintenanceRequest, id string) (diagnosticAgentStopScope, error) {
	fail := diagnosticAgentStopUnavailable()
	before, err := diagnostics.CaptureManualCleanupScope(ctx, s.store, q, s.integrations)
	if err != nil {
		return diagnosticAgentStopScope{}, fail
	}
	task, ok := uniqueCleanupTask(s.filteredTasks(), before.TaskID)
	if !ok || !diagnostics.ManualCleanupTaskInScope(task, q.Workspace) || len(task.SourceRefs) > 256 {
		return diagnosticAgentStopScope{}, fail
	}
	var selected protocol.SourceRef
	matches := 0
	for _, ref := range task.SourceRefs {
		if ref.AgentID == id {
			selected = ref
			matches++
		}
	}
	if matches != 1 || !selected.IsDurableAgent() || selected.ID != "agent:instance:"+id || selected.EntityID != selected.ID || selected.Metadata["agent_id"] != id {
		return diagnosticAgentStopScope{}, fail
	}
	raw, err := json.Marshal(selected)
	if err != nil || len(raw) > 64*1024 {
		return diagnosticAgentStopScope{}, fail
	}
	var groups []protocol.DiagnosticWorkspace
	if json.Unmarshal([]byte(before.Registration), &groups) != nil {
		return diagnosticAgentStopScope{}, fail
	}
	var binding protocol.AgentWorkspaceBinding
	for _, group := range groups {
		if group.ID == selected.Metadata["workspace_id"] && group.Path == selected.Metadata["workspace_path"] {
			binding = protocol.AgentWorkspaceBinding{ID: group.ID, Path: group.Path, Mode: "local"}
			if group.SandboxName != "" {
				binding.Mode, binding.Sandbox = "sbx", group.SandboxName
			}
		}
	}
	if binding.ID == "" || binding.Mode != selected.Metadata["execution"] || (q.Scope != "installation" && binding.Path != q.Workspace) || ctx.Err() != nil || s.store.Revision() != before.Revision {
		return diagnosticAgentStopScope{}, fail
	}
	return diagnosticAgentStopScope{cleanup: before, workspace: binding, ref: sha256.Sum256(raw)}, nil
}

// Preparation retains one native capture with its original final guard in the
// existing per-task cleanupPlans map. It does not execute admission or persist
// anything. Stop and cleanup supersede each other; there is no second cache.
func (s *Server) prepareDiagnosticAgentStop(ctx context.Context, q protocol.DiagnosticMaintenanceRequest, id string) (diagnosticAgentStopView, error) {
	fail := func() (diagnosticAgentStopView, error) {
		return diagnosticAgentStopView{}, diagnosticAgentStopUnavailable()
	}
	if !validDiagnosticAgentStopRequest(q, id) || q.Mode != "preview" || ctx.Err() != nil || s.resourceSnapshot == nil || s.markResourcesPending == nil || !s.maintenanceMu.TryLock() {
		return fail()
	}
	defer s.maintenanceMu.Unlock()
	if s.refreshPlan != nil && s.refreshPlan.view.Status == "running" {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	provider, err := s.integrations.AgentStopDiagnostics()
	if err != nil {
		return fail()
	}
	before, err := s.captureDiagnosticAgentStopScope(ctx, q, id)
	if err != nil || !s.diagnosticRecreationInputsLive(ctx, before.cleanup.Revision, time.Time{}) {
		return fail()
	}
	p := &diagnosticAgentStopPlan{request: cleanupBindingRequest(q), scope: before}
	// No replaceable apply-time callback. This guard checks exact daemon scope
	// and publication AFTER native revalidation, immediately before effects. It
	// may run only synchronously inside this adapter's serialized consumed apply.
	guard := func(c context.Context, execute func() error) error {
		if !p.active.Load() || !p.entered.CompareAndSwap(false, true) {
			return diagnosticAgentStopUnavailable()
		}
		current, err := s.captureDiagnosticAgentStopScope(c, q, id)
		if err != nil || current != before || !s.diagnosticRecreationInputsLive(c, before.cleanup.Revision, p.expires) || !p.active.Load() {
			return diagnosticAgentStopUnavailable()
		}
		p.operation = s.store.BeginOperation("agent-stop", before.cleanup.TaskID, before.workspace.ID)
		p.dispatched.Store(true)
		return execute()
	}
	native, err := provider.PrepareDiagnosticAgentStop(ctx, id, before.workspace, guard)
	if err != nil || native == nil {
		return fail()
	}
	p.native = native
	preview, expires, nativeBytes := native.Preview(), native.ExpiresAt(), native.RetainedBytes()
	p.expires = expires
	if preview.ID != id || nativeBytes < 1 || nativeBytes > maxCleanupPlanBytes || expires.IsZero() || expires.After(time.Now().Add(30*time.Second)) {
		return fail()
	}
	after, err := s.captureDiagnosticAgentStopScope(ctx, q, id)
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
	p.view = diagnosticAgentStopView{agent: id, record: before.cleanup.RecordID, taskID: before.cleanup.TaskID, feedback: before.cleanup.FeedbackID, workspace: before.workspace.ID, revision: before.cleanup.Revision, desired: preview.Desired, running: preview.Running, planID: hex.EncodeToString(token[:]), expiresAt: expires.UTC().Format(time.RFC3339Nano), mode: "preview", problem: "none", outcome: "native_exact_stop_plan_not_consent"}
	err = s.retainCleanupPlan(ctx, p.view.taskID, p.view.revision, func(now time.Time) (nativeCleanupPlan, error) {
		if !now.Before(expires) {
			return nativeCleanupPlan{}, diagnosticAgentStopUnavailable()
		}
		return nativeCleanupPlan{id: p.view.planID, revision: p.view.revision, expires: expires, agentStop: p}, nil
	})
	if err != nil {
		return fail()
	}
	return p.view, nil
}

// Trusted callers must enforce fresh exact interruption consent separately.
// The public CLI/Pi control surface is not enabled by the daemon transport.
// Consumption precedes scope/cancellation/expiry checks. Lost replies and all
// failures leave no reusable plan; no rollback or safe retry is implied.
func (s *Server) applyDiagnosticAgentStop(ctx context.Context, q protocol.DiagnosticMaintenanceRequest, id string) (diagnosticAgentStopView, error) {
	fail := func() (diagnosticAgentStopView, error) {
		return diagnosticAgentStopView{}, diagnosticAgentStopUnavailable()
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
	p := retained.agentStop
	if p == nil || !validDiagnosticAgentStopRequest(q, id) || q.Mode != "apply" || p.request != cleanupBindingRequest(q) || p.view.agent != id || ctx.Err() != nil || s.markResourcesPending == nil || s.resourceSnapshot == nil || !s.diagnosticCleanupInputsLive(ctx, retained.expires, retained.revision) || (s.refreshPlan != nil && s.refreshPlan.view.Status == "running") {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p.active.Store(true)
	defer p.active.Store(false)
	err := p.native.Execute(ctx)
	view := p.view
	view.mode, view.dispatched, view.operationID = "apply", p.dispatched.Load(), p.operation
	if !view.dispatched {
		return fail()
	}
	// Desired-state persistence may precede a failed shutdown. Even a failed or
	// cancelled native return invalidates cached resources once dispatch began.
	s.markResourcesPending()
	view.outcome = "native_handler_returned_unverified"
	if err != nil || ctx.Err() != nil {
		view.problem, view.outcome = "native_error", "unverified_may_be_partial"
		if ctx.Err() != nil {
			view.problem = "cancelled_or_timed_out"
		}
	}
	status := "succeeded"
	if view.problem != "none" {
		status = "failed"
	}
	s.store.FinishOperation(view.operationID, protocol.ManagedOperation{Status: status, TaskID: view.taskID, WorkspaceID: view.workspace})
	return view, nil
}

// The exact native capture/executor remains private; project only fixed facts.
// No approval, native plan, arbitrary method or target operation crosses the wire.
func (s *Server) diagnosticAgentStop(ctx context.Context, q protocol.DiagnosticMaintenanceRequest) (protocol.DiagnosticAgentStop, error) {
	var private diagnosticAgentStopView
	var err error
	if q.Mode == "preview" {
		private, err = s.prepareDiagnosticAgentStop(ctx, q, q.AgentID)
	} else {
		private, err = s.applyDiagnosticAgentStop(ctx, q, q.AgentID)
	}
	if err != nil {
		return protocol.DiagnosticAgentStop{}, diagnosticAgentStopUnavailable()
	}
	view := protocol.DiagnosticAgentStop{Schema: protocol.DiagnosticsVersion, Operation: "agent_stop", Mode: private.mode, Scope: q.Scope, RecordID: private.record, TaskID: private.taskID, AgentID: private.agent, FeedbackWorkspaceID: private.feedback, AgentWorkspaceID: private.workspace, Evidence: "native_exact_agent_stop_plan_not_consent", PlanID: private.planID, ExpiresAt: private.expiresAt, Revision: private.revision, DesiredBefore: private.desired, RunningBefore: private.running, DispatchEvidence: "not_dispatched", Problem: private.problem, Outcome: "not_attempted", OperationID: private.operationID, ResourceRefreshPending: private.dispatched, Limitations: protocol.DiagnosticAgentStopLimitations()}
	if q.Scope != "installation" {
		view.ScopeWorkspaceID = private.workspace
	}
	if private.dispatched {
		view.DispatchEvidence, view.Outcome = "native_stop_callback_entered_not_process_outcome", private.outcome
	}
	if !protocol.ValidDiagnosticAgentStop(view, q) {
		if q.Mode == "preview" {
			s.discardCleanupPlan(&protocol.CleanupPreview{TaskID: private.taskID, PlanID: private.planID})
		}
		return protocol.DiagnosticAgentStop{}, diagnosticAgentStopUnavailable()
	}
	return view, nil
}
