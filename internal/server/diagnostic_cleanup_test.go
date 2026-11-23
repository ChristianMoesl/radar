package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/client"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type manualAdapterSource struct {
	groups                                            []protocol.DiagnosticWorkspace
	targets                                           []protocol.CleanupTarget
	received                                          []protocol.CleanupTarget
	previews, admissions, preflights, releases, calls atomic.Int32
	hidden                                            atomic.Bool
	failAt                                            int32
	rejectAdmission                                   bool
	onPreview                                         func(context.Context)
	onCleanup                                         func(context.Context, int32)
	onFilter                                          func()
	transform                                         func([]protocol.Task) []protocol.Task
	onRegistration                                    func()
	borrowTargets                                     bool
}

func (*manualAdapterSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "git"}
}
func (*manualAdapterSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	panic("collection")
}
func (p *manualAdapterSource) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	if p.onRegistration != nil {
		p.onRegistration()
	}
	raw, _ := json.Marshal(p.groups)
	var groups []protocol.DiagnosticWorkspace
	_ = json.Unmarshal(raw, &groups)
	return groups, nil
}
func (p *manualAdapterSource) FilterTasks(tasks []protocol.Task, _ *slog.Logger) []protocol.Task {
	if p.onFilter != nil {
		p.onFilter()
	}
	if p.hidden.Load() {
		return nil
	}
	if p.transform != nil {
		return p.transform(tasks)
	}
	return tasks
}
func (p *manualAdapterSource) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if req.Mode != integration.CleanupConfirmed {
		panic("not manual")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
		panic("unbounded")
	}
	p.previews.Add(1)
	if p.onPreview != nil {
		p.onPreview(ctx)
	}
	if p.borrowTargets {
		return p.targets, nil
	}
	return cloneCleanupPreview(protocol.CleanupPreview{Targets: p.targets}).Targets, nil
}
func (p *manualAdapterSource) AdmitCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	p.admissions.Add(1)
	if p.rejectAdmission {
		return nil, nil, errors.New("private-alias-admission")
	}
	return p, func() { p.releases.Add(1) }, nil
}
func (p *manualAdapterSource) PreflightCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error {
	p.preflights.Add(1)
	return nil
}
func (p *manualAdapterSource) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	n := p.calls.Add(1)
	p.received = append(p.received, cloneCleanupPreview(protocol.CleanupPreview{Targets: []protocol.CleanupTarget{req.Target}}).Targets[0])
	if p.onCleanup != nil {
		p.onCleanup(ctx, n)
	}
	if p.failAt == n {
		return req.Target, errors.New("private-provider-failure")
	}
	return req.Target, nil
}
func manualAdapterFixture(t *testing.T) (*Server, *manualAdapterSource, protocol.DiagnosticMaintenanceRequest) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	p := &manualAdapterSource{}
	origin, feedback, other := filepath.Join(root, "origin"), filepath.Join(root, "feedback"), filepath.Join(root, "other")
	for _, path := range []string{origin, feedback, other} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	p.groups = []protocol.DiagnosticWorkspace{{ID: "origin", Path: origin, Registered: true}, {ID: "feedback", Path: feedback, Registered: true, Feedback: true, FeedbackOrigin: origin}, {ID: "other", Path: other, Registered: true}}
	p.targets = []protocol.CleanupTarget{{Source: "git", Kind: "worktree", SourceRefID: "git:ABC-123", ResourceID: "private-first", WorkspaceID: "origin", Operation: map[string]string{"delete_branch": "private-branch"}, Safety: []protocol.CleanupSafety{{Kind: "local_changes", Message: "private-warning", BlocksAutomatic: true}}}, {Source: "git", Kind: "worktree", ResourceID: "private-second", WorkspaceID: "other", Operation: map[string]string{"command": "private-command"}}}
	s, store := refreshFixture(t, p)
	s.cleanupService = cleanup.New(s.integrations.CleanupProviders())
	store.SetTasks([]protocol.Task{{Title: "private-title", Attention: "done", SourceRefs: []protocol.SourceRef{{ID: "git:ABC-123", Source: "git", Kind: "worktree", Path: origin, Role: protocol.SourceRefRoleAuthoritative}, {ID: "git:ABC-124", Source: "git", Kind: "worktree", Path: other, Role: protocol.SourceRefRoleAuthoritative}}}})
	records, _, _ := store.InspectionSnapshot()
	s.SetResourcePublication(func() {}, func() protocol.Response { return protocol.Response{} })
	return s, p, protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: "preview", Scope: "origin", ParentID: records[0].ID, Workspace: origin, FeedbackWorkspace: feedback}
}
func mustManualAdapterPreview(t *testing.T, s *Server, req protocol.DiagnosticMaintenanceRequest) protocol.DiagnosticCleanup {
	t.Helper()
	v, err := s.diagnosticCleanup(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func applyManualAdapterRequest(req protocol.DiagnosticMaintenanceRequest, view protocol.DiagnosticCleanup) protocol.DiagnosticMaintenanceRequest {
	req.Mode, req.PlanID = "apply", view.PlanID
	return req
}

func TestDiagnosticManualCleanupCompleteDisclosureAndSingleManagedExecutor(t *testing.T) {
	s, p, req := manualAdapterFixture(t)
	before := s.store.Revision()
	preview := mustManualAdapterPreview(t, s, req)
	if preview.TaskID < 1 || preview.RecordID != req.ParentID || len(preview.Targets) != 2 || preview.Targets[0].WorkspaceID != "origin" || preview.Targets[1].WorkspaceID != "other" || preview.Targets[0].BranchEffect != "delete_local_branch" || preview.Targets[0].SafetyNoticeCount != 1 || preview.PlanID == "" || preview.OperationID != "" || preview.CompletedTargets != nil || p.previews.Load() != 1 || p.admissions.Load() != 0 || p.calls.Load() != 0 || s.store.Revision() != before {
		t.Fatalf("incomplete or side-effectful preview: %+v", preview)
	}
	raw, _ := json.Marshal(preview)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), req.Workspace) {
		t.Fatal("private preview escaped")
	}
	applied, err := s.diagnosticCleanup(context.Background(), applyManualAdapterRequest(req, preview))
	if err != nil || applied.Status != "returned" || applied.Problem != "none" || applied.OperationID == "" || !applied.ResourceRefreshPending || applied.CompletedTargets == nil || *applied.CompletedTargets != 2 || applied.Execution == nil || applied.Execution.Stage != "completed" || p.previews.Load() != 1 || p.calls.Load() != 2 || p.admissions.Load() != 1 || p.releases.Load() != 1 {
		t.Fatalf("unmanaged/invalid cleanup %+v %v", applied, err)
	}
	_, _, operations, _ := s.store.OperationInspectionSnapshot()
	if len(operations) != 1 || operations[0].ID != applied.OperationID || operations[0].Kind != "cleanup" || operations[0].CompletedTargets != 2 {
		t.Fatalf("lost managed evidence %+v", operations)
	}
	if _, err := s.diagnosticCleanup(context.Background(), applyManualAdapterRequest(req, preview)); err == nil || p.calls.Load() != 2 {
		t.Fatal("replayed plan")
	}
}
func TestDiagnosticManualCleanupUsesImmutableNativeAndPublicPlan(t *testing.T) {
	s, p, req := manualAdapterFixture(t)
	view := mustManualAdapterPreview(t, s, req)
	view.Targets[0].Effect = "private-injected"
	p.targets[0].Operation["delete_branch"] = "changed-after-preview"
	p.targets = p.targets[:1]
	// Capture the executor's original immutable target, not the mutated provider preview input.
	p.onCleanup = func(_ context.Context, n int32) {
		if n == 1 {
			p.targets = nil
		}
	}
	applied, err := s.diagnosticCleanup(context.Background(), applyManualAdapterRequest(req, view))
	if err != nil || len(applied.Targets) != 2 || applied.Targets[0].Effect != "remove_worktree_and_local_changes" || p.calls.Load() != 2 || p.received[0].Operation["delete_branch"] != "private-branch" {
		t.Fatal("mutable or silently replanned", applied, err)
	}
}
func TestDiagnosticManualCleanupScopeAndProductionAvailabilityBeforeProviderReads(t *testing.T) {
	for _, kind := range []string{"hidden", "missing_parent", "wrong_scope", "not_feedback", "changed_origin", "unregistered_scope", "duplicate_path", "duplicate_id", "no_feedback", "pending", "no_snapshot", "busy", "cancelled", "refresh_running", "bad_mode", "bad_schema", "preview_plan", "oversized_parent", "wrong_operation"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req := manualAdapterFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "hidden":
				p.hidden.Store(true)
			case "missing_parent":
				req.ParentID = "missing"
			case "wrong_scope":
				req.Workspace = p.groups[2].Path
			case "not_feedback":
				p.groups[1].Feedback = false
			case "changed_origin":
				p.groups[1].FeedbackOrigin = p.groups[2].Path
			case "unregistered_scope":
				req.Workspace = filepath.Join(t.TempDir(), "outside")
			case "duplicate_path":
				p.groups = append(p.groups, protocol.DiagnosticWorkspace{ID: "duplicate", Path: req.Workspace, Registered: true})
			case "duplicate_id":
				p.groups[2].ID = p.groups[0].ID
			case "no_feedback":
				req.FeedbackWorkspace = ""
			case "pending":
				s.resourceSnapshot = func() protocol.Response { return protocol.Response{ResourceRefreshPending: true} }
			case "no_snapshot":
				s.resourceSnapshot = nil
			case "busy":
				s.maintenanceMu.Lock()
				defer s.maintenanceMu.Unlock()
			case "cancelled":
				cancel()
			case "refresh_running":
				s.refreshPlan = &diagnosticRefreshPlan{view: protocol.DiagnosticMaintenance{Status: "running"}}
			case "bad_mode":
				req.Mode = "expired"
			case "bad_schema":
				req.Schema--
			case "preview_plan":
				req.PlanID = strings.Repeat("a", 64)
			case "oversized_parent":
				req.ParentID = strings.Repeat("a", 513)
			case "wrong_operation":
				req.Operation = "refresh"
			}
			if _, err := s.diagnosticCleanup(ctx, req); err == nil || p.previews.Load() != 0 || p.calls.Load() != 0 || len(s.cleanupPlans) != 0 {
				t.Fatal("unsafe scope reached planner", kind, err)
			}
		})
	}
}
func TestDiagnosticManualCleanupPlanConsumptionAndFreshInputs(t *testing.T) {
	for _, kind := range []string{"registration", "revision", "pending", "hidden", "scope", "parent_alias", "feedback", "expired", "restart", "native_supersedes", "diagnostic_supersedes", "cancelled_during_filter", "registration_during_filter", "expiry_during_filter"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req := manualAdapterFixture(t)
			view := mustManualAdapterPreview(t, s, req)
			apply := applyManualAdapterRequest(req, view)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "registration":
				p.groups[2].NotePath = "private-changed"
			case "revision":
				s.store.SetTasks(s.store.Tasks())
			case "pending":
				s.resourceSnapshot = func() protocol.Response { return protocol.Response{ResourceRefreshPending: true} }
			case "hidden":
				p.hidden.Store(true)
			case "scope":
				apply.Scope = "installation"
				apply.Workspace = ""
			case "parent_alias":
				apply.ParentID = "1"
			case "feedback":
				apply.FeedbackWorkspace = p.groups[0].Path
			case "expired":
				plan := s.cleanupPlans[view.TaskID]
				plan.expires = time.Now().Add(-time.Second)
				s.cleanupPlans[view.TaskID] = plan
			case "restart":
				s.cleanupPlans = nil
			case "native_supersedes":
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if _, err := s.cleanupPreview(ctx, view.TaskID); err != nil {
					t.Fatal(err)
				}
			case "diagnostic_supersedes":
				mustManualAdapterPreview(t, s, req)
			case "cancelled_during_filter":
				p.onFilter = cancel
			case "registration_during_filter":
				p.onFilter = func() { p.groups[2].NotePath = "private-filter-drift" }
			case "expiry_during_filter":
				plan := s.cleanupPlans[view.TaskID]
				plan.expires = time.Now().Add(20 * time.Millisecond)
				s.cleanupPlans[view.TaskID] = plan
				p.onFilter = func() { time.Sleep(25 * time.Millisecond) }
			}
			if _, err := s.diagnosticCleanup(ctx, apply); err == nil || p.calls.Load() != 0 || p.admissions.Load() != 0 {
				t.Fatal("stale context executed", kind, err)
			}
			for _, plan := range s.cleanupPlans {
				if plan.id == view.PlanID {
					t.Fatal("rejected apply retained its old identity")
				}
			}
			// A rejected old identity stays unusable even if supplied again.
			if _, err := s.diagnosticCleanup(context.Background(), applyManualAdapterRequest(req, view)); err == nil || p.calls.Load() != 0 {
				t.Fatal("old plan revived")
			}
		})
	}
}
func TestDiagnosticManualCleanupPlannerDriftAndCompleteBounds(t *testing.T) {
	for _, kind := range []string{"registration", "revision", "cancelled", "pending", "unsupported_effect", "unsupported_provider", "unknown_workspace_identity", "oversized_public", "duplicate_targets", "lease_contention"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req := manualAdapterFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "registration":
				p.onPreview = func(context.Context) { p.groups[2].NotePath = "private-drift" }
			case "revision":
				p.onPreview = func(context.Context) { s.store.SetTasks(s.store.Tasks()) }
			case "cancelled":
				p.onPreview = func(context.Context) { cancel() }
			case "pending":
				pending := false
				p.onPreview = func(context.Context) { pending = true }
				s.resourceSnapshot = func() protocol.Response { return protocol.Response{ResourceRefreshPending: pending} }
			case "unsupported_effect":
				p.targets[1].Kind = "unknown"
			case "unsupported_provider":
				p.targets[1].Source = "unknown"
			case "unknown_workspace_identity":
				p.targets[1].WorkspaceID = "private-unknown-workspace"
			case "oversized_public":
				for len(p.targets) < 100 {
					p.targets = append(p.targets, p.targets[0])
				}
			case "duplicate_targets":
				p.targets = append(p.targets, p.targets[0])
			case "lease_contention":
				p.targets[0].Path = req.Workspace
				release, err := workspacelease.Acquire(req.Workspace, false)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			v, err := s.diagnosticCleanup(ctx, req)
			if kind == "duplicate_targets" {
				if err != nil || len(v.Targets) != 3 || v.Targets[0].ID == v.Targets[2].ID {
					t.Fatal("duplicate native ordinal dropped", v, err)
				}
				return
			}
			if err == nil || len(s.cleanupPlans) != 0 || p.calls.Load() != 0 {
				t.Fatal("bad disclosure issued plan", kind, v, err)
			}
		})
	}
}
func TestDiagnosticManualCleanupPartialDispatchCancellationAndNativeAliasRefusal(t *testing.T) {
	for _, kind := range []string{"first_failure", "second_failure", "cancel_after_first", "native_alias"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req := manualAdapterFixture(t)
			view := mustManualAdapterPreview(t, s, req)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stage, completed, attempted := "provider_call", 0, 1
			switch kind {
			case "first_failure":
				p.failAt = 1
			case "second_failure":
				p.failAt = 2
				completed, attempted = 1, 2
			case "cancel_after_first":
				p.onCleanup = func(context.Context, int32) { cancel() }
				stage, completed, attempted = "target_dispatch", 1, 1
			case "native_alias":
				p.rejectAdmission = true
				stage, attempted = "ownership_and_leases", 0
			}
			result, err := s.diagnosticCleanup(ctx, applyManualAdapterRequest(req, view))
			if err != nil || result.OperationID == "" || !result.ResourceRefreshPending || result.CompletedTargets == nil || *result.CompletedTargets != completed || result.Execution == nil || result.Execution.Stage != stage || result.Execution.AttemptedTargets != attempted {
				t.Fatalf("lost partial boundary %+v %v", result, err)
			}
			if kind != "native_alias" && p.releases.Load() != 1 {
				t.Fatal("admission leaked")
			}
			if kind == "cancel_after_first" && result.Problem != "cancelled_or_timed_out" {
				t.Fatal("cancellation mislabelled")
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "private-") {
				t.Fatal("private failure payload escaped")
			}
			calls := p.calls.Load()
			if _, err := s.diagnosticCleanup(context.Background(), applyManualAdapterRequest(req, view)); err == nil || p.calls.Load() != calls {
				t.Fatal("partial plan replay")
			}
		})
	}
}
func TestDiagnosticManualCleanupConcurrentApplyDispatchesAtMostOnce(t *testing.T) {
	s, p, req := manualAdapterFixture(t)
	v := mustManualAdapterPreview(t, s, req)
	apply := applyManualAdapterRequest(req, v)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.diagnosticCleanup(context.Background(), apply); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || p.calls.Load() != 2 || p.previews.Load() != 1 {
		t.Fatal("duplicate apply", successes.Load(), p.calls.Load())
	}
}

func TestDiagnosticManualCleanupFreshOwnerAndLeaseGuards(t *testing.T) {
	for _, kind := range []string{"stopped_before_preview", "active_before_preview", "stopped_after_preview", "lease_after_preview", "invalid_owner_evidence"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req := manualAdapterFixture(t)
			p.targets[1].Path = p.groups[2].Path // A different workspace still blocks the whole bundle.
			var view protocol.DiagnosticCleanup
			if kind == "stopped_after_preview" || kind == "lease_after_preview" {
				view = mustManualAdapterPreview(t, s, req)
			}
			if kind == "lease_after_preview" {
				release, err := workspacelease.Acquire(p.targets[1].Path, false)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			} else {
				owner := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "abc-owner")
				if err := os.MkdirAll(owner, 0700); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(agentservice.Registration{Version: 1, ID: "abc-owner", Desired: kind == "active_before_preview", Entrypoint: "fixture", Workspace: protocol.AgentWorkspaceBinding{ID: "other", Path: p.targets[1].Path, Mode: "local"}})
				if err != nil {
					t.Fatal(err)
				}
				if kind == "invalid_owner_evidence" {
					raw = []byte("private-invalid-registration")
				}
				if err := os.WriteFile(filepath.Join(owner, "registration.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if view.PlanID == "" {
				if _, err := s.diagnosticCleanup(context.Background(), req); err == nil || len(s.cleanupPlans) != 0 {
					t.Fatal("owner/unknown evidence issued a plan")
				}
			} else {
				result, err := s.diagnosticCleanup(context.Background(), applyManualAdapterRequest(req, view))
				if err != nil || result.Execution == nil || result.Execution.Stage != "ownership_and_leases" || result.Problem != "native_error" || result.CompletedTargets == nil || *result.CompletedTargets != 0 {
					t.Fatal("fresh blocker lost native evidence", result, err)
				}
				if _, err := s.diagnosticCleanup(context.Background(), applyManualAdapterRequest(req, view)); err == nil {
					t.Fatal("blocked plan replay")
				}
			}
			if p.calls.Load() != 0 || p.admissions.Load() != 0 {
				t.Fatal("owner/lease blocker reached provider admission")
			}
		})
	}
}

func TestDiagnosticManualCleanupScopeSelectorsAndNativePlanIsolation(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		t.Run(scope, func(t *testing.T) {
			s, p, req := manualAdapterFixture(t)
			req.Scope = scope
			switch scope {
			case "feedback":
				req.Workspace = req.FeedbackWorkspace
				tasks := s.store.Tasks()
				tasks[0].SourceRefs[0].Path = req.FeedbackWorkspace
				s.store.SetTasks(tasks)
			case "installation":
				req.Workspace = ""
			}
			view := mustManualAdapterPreview(t, s, req)
			if view.Scope != scope || view.FeedbackWorkspaceID != "feedback" || len(view.Targets) != 2 {
				t.Fatal("incorrect scope", view)
			}
			// A generic cleanup request must not use a diagnostic context to skip
			// exact scope/registration revalidation, even with the private payload.
			var native protocol.CleanupPreview
			if err := json.Unmarshal(s.cleanupPlans[view.TaskID].payload, &native); err != nil {
				t.Fatal(err)
			}
			if _, err := s.cleanup(context.Background(), &native); err == nil || p.calls.Load() != 0 {
				t.Fatal("generic route bypassed typed context")
			}
			if _, err := s.diagnosticCleanup(context.Background(), applyManualAdapterRequest(req, view)); err == nil {
				t.Fatal("rejected generic request did not consume plan")
			}
		})
	}
}

func TestDiagnosticManualCleanupPublicationReaderCannotOutliveApplyAdmission(t *testing.T) {
	for _, kind := range []string{"cancelled", "revision", "pending"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req := manualAdapterFixture(t)
			view := mustManualAdapterPreview(t, s, req)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads := 0
			s.resourceSnapshot = func() protocol.Response {
				reads++
				if reads == 2 {
					switch kind {
					case "cancelled":
						cancel()
					case "revision":
						s.store.SetTasks(s.store.Tasks())
					case "pending":
						return protocol.Response{ResourceRefreshPending: true}
					}
				}
				return protocol.Response{}
			}
			if _, err := s.diagnosticCleanup(ctx, applyManualAdapterRequest(req, view)); err == nil || reads != 2 || p.calls.Load() != 0 || p.admissions.Load() != 0 {
				t.Fatal("publication callback crossed admission", err, reads)
			}
			_, _, operations, _ := s.store.OperationInspectionSnapshot()
			if len(operations) != 0 {
				t.Fatal("rejected live admission started the managed handler")
			}
			if len(s.cleanupPlans) != 0 {
				t.Fatal("cancelled/changed apply retained identity")
			}
		})
	}
}

func TestDiagnosticManualCleanupRequiresCurrentUnambiguousProductionScope(t *testing.T) {
	for _, kind := range []string{"historical_scope_only", "duplicate_current_identity", "current_reference_budget"} {
		for _, phase := range []string{"preview", "apply"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				s, p, req := manualAdapterFixture(t)
				var view protocol.DiagnosticCleanup
				if phase == "apply" {
					view = mustManualAdapterPreview(t, s, req)
				}
				p.transform = func(tasks []protocol.Task) []protocol.Task {
					switch kind {
					case "historical_scope_only":
						for i := range tasks[0].SourceRefs {
							tasks[0].SourceRefs[i].Path = p.groups[2].Path
							tasks[0].SourceRefs[i].Metadata = nil
						}
					case "duplicate_current_identity":
						tasks = append(tasks, tasks[0])
					case "current_reference_budget":
						for len(tasks[0].SourceRefs) < 257 {
							tasks[0].SourceRefs = append(tasks[0].SourceRefs, tasks[0].SourceRefs[0])
						}
					}
					return tasks
				}
				request := req
				if phase == "apply" {
					request = applyManualAdapterRequest(req, view)
				}
				if _, err := s.diagnosticCleanup(context.Background(), request); err == nil || p.calls.Load() != 0 || p.admissions.Load() != 0 {
					t.Fatal("retained identity bypassed canonical current task", err)
				}
				if phase == "preview" && p.previews.Load() != 0 {
					t.Fatal("invalid production task reached provider reads")
				}
			})
		}
	}
}

func TestDiagnosticManualCleanupFreezesNativeOperationsBeforeDisclosureCallbacks(t *testing.T) {
	s, p, req := manualAdapterFixture(t)
	p.borrowTargets = true
	p.onRegistration = func() {
		if p.previews.Load() > 0 {
			p.targets[0].Operation["delete_branch"] = "private-provider-changed-after-disclosure"
			p.targets[0].Safety[0].BlocksAutomatic = false
		}
	}
	view := mustManualAdapterPreview(t, s, req)
	result, err := s.diagnosticCleanup(context.Background(), applyManualAdapterRequest(req, view))
	if err != nil || result.Problem != "none" || len(p.received) != 2 || p.received[0].Operation["delete_branch"] != "private-branch" || !p.received[0].Safety[0].BlocksAutomatic {
		t.Fatal("provider mutation silently replaced disclosed private operations", result, err)
	}
}

func TestDiagnosticManualCleanupTypedRPCAndPartialManagedResult(t *testing.T) {
	s, p, req := manualAdapterFixture(t)
	response := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &req})
	if !response.OK || response.DiagnosticCleanup == nil || response.CleanupPreview != nil || response.Diagnostics != nil || response.Maintenance != nil {
		t.Fatal("missing typed preview", response)
	}
	view := *response.DiagnosticCleanup
	p.failAt = 2
	apply := applyManualAdapterRequest(req, view)
	response = refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &apply})
	v := response.DiagnosticCleanup
	if !response.OK || v == nil || v.Problem != "native_error" || v.OperationID == "" || v.CompletedTargets == nil || *v.CompletedTargets != 1 || v.Execution == nil || v.Execution.Stage != "provider_call" || v.Execution.AttemptedTargets != 2 || !v.ResourceRefreshPending || response.CleanupResult != nil || response.Error != "" {
		t.Fatal("raw/unmanaged partial result", response)
	}
	raw, _ := json.Marshal(response)
	if strings.Contains(string(raw), "private-") {
		t.Fatal("private native payload escaped RPC")
	}
	if response = refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &apply}); response.OK || p.calls.Load() != 2 {
		t.Fatal("RPC replay")
	}
}

func TestDiagnosticManualCleanupTypedWireRejectsUnexpectedFieldsBeforeReads(t *testing.T) {
	for _, kind := range []string{"unknown_envelope", "client_targets", "subset", "cursor", "limit", "state", "cleanup_mode", "workspace_id", "duplicate_mode", "folded_scope", "old_schema", "empty_preview_plan", "null_preview_plan", "wrong_mode", "wrong_operation", "missing_feedback", "manual_probe"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req := manualAdapterFixture(t)
			raw, _ := json.Marshal(protocol.Request{Method: "diagnostics-maintenance", Maintenance: &req})
			frame := string(raw)
			switch kind {
			case "unknown_envelope":
				frame = strings.TrimSuffix(frame, "}") + `,"private":"secret"}`
			case "client_targets", "subset", "cursor", "limit", "state", "cleanup_mode", "workspace_id":
				frame = strings.Replace(frame, `"maintenance":{`, `"maintenance":{"`+kind+`":"private",`, 1)
			case "duplicate_mode":
				frame = strings.Replace(frame, `"mode":"preview"`, `"mode":"preview","mode":"apply"`, 1)
			case "folded_scope":
				frame = strings.Replace(frame, `"scope":"origin"`, `"scope":"origin","SCOPE":"installation"`, 1)
			case "old_schema":
				req.Schema--
				raw, _ = json.Marshal(protocol.Request{Method: "diagnostics-maintenance", Maintenance: &req})
				frame = string(raw)
			case "empty_preview_plan":
				frame = strings.Replace(frame, `"maintenance":{`, `"maintenance":{"plan_id":"",`, 1)
			case "null_preview_plan":
				frame = strings.Replace(frame, `"maintenance":{`, `"maintenance":{"plan_id":null,`, 1)
			case "wrong_mode":
				frame = strings.Replace(frame, `"mode":"preview"`, `"mode":"expired"`, 1)
			case "wrong_operation":
				frame = strings.Replace(frame, `"operation":"cleanup"`, `"operation":"refresh"`, 1)
			case "missing_feedback":
				req.FeedbackWorkspace = ""
				raw, _ = json.Marshal(protocol.Request{Method: "diagnostics-maintenance", Maintenance: &req})
				frame = string(raw)
			case "manual_probe":
				raw, _ = json.Marshal(protocol.Request{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: req.ParentID, Workspace: req.Workspace, CleanupMode: "manual"}})
				frame = string(raw)
			}
			response := rawCheckRPC(t, s, frame)
			if response.OK || response.DiagnosticCleanup != nil || p.previews.Load() != 0 || p.calls.Load() != 0 {
				t.Fatal("invalid wire reached planner", kind, response)
			}
		})
	}
}

func TestDiagnosticManualCleanupNativeClientRoundTrip(t *testing.T) {
	s, p, req := manualAdapterFixture(t)
	dir, err := os.MkdirTemp("", "rmc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			s.handle(conn)
		}
		done <- nil
	}()
	response, err := client.MaintainDiagnostics(path, req, 2*time.Second)
	if err != nil || !response.OK || response.DiagnosticCleanup == nil {
		t.Fatal("client preview unavailable", response, err)
	}
	apply := applyManualAdapterRequest(req, *response.DiagnosticCleanup)
	response, err = client.MaintainDiagnostics(path, apply, 2*time.Second)
	if err != nil || !response.OK || response.DiagnosticCleanup == nil || response.DiagnosticCleanup.OperationID == "" || p.calls.Load() != 2 {
		t.Fatal("client apply unavailable", response, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
