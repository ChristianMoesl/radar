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

// Private adapter infrastructure only. No protocol handler, CLI, Pi control or
// consent is enabled here. A public adapter still needs its finite disclosure,
// version-matched strict transport and separate fresh registration-removal approval.
// All fields are private: accidentally marshaling a plan or view yields {}.
type diagnosticAgentRemoveView struct {
	schedules, enabledSchedules                           int
	agent, record, feedback, workspace, planID, expiresAt string
	taskID                                                int
	revision                                              int64
	dispatched                                            bool
	mode, problem, outcome                                string
}
type diagnosticAgentRemovePlan struct {
	request                     protocol.DiagnosticMaintenanceRequest
	scope                       diagnosticAgentScope
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

// In-process execution only, deliberately not wired to an RPC or public client.
// Future transport must enforce fresh exact registration-removal consent separately.
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
	err := p.native.Execute(ctx)
	view := p.view
	view.mode, view.dispatched = "apply", p.dispatched.Load()
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
	return view, nil
}
