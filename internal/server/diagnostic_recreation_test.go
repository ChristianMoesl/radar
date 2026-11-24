package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type recreationAdmissionSource struct {
	groups                            []protocol.DiagnosticWorkspace
	registrations, prepares, executes int
	root                              string
	owners                            int
	lease, status                     string
	captured                          integration.SandboxRecreateRequest
	registrationHook                  func(int)
	prepareHook                       func(context.Context)
	executeHook                       func(context.Context) (integration.SandboxRecreateResult, error)
}

func (*recreationAdmissionSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "workspace"}
}
func (p *recreationAdmissionSource) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	p.registrations++
	if p.registrationHook != nil {
		p.registrationHook(p.registrations)
	}
	return append([]protocol.DiagnosticWorkspace{}, p.groups...), nil
}
func (*recreationAdmissionSource) DiagnosticRecreationPreview(context.Context, string, string) (protocol.DiagnosticRecreationPreview, error) {
	panic("discarding read adapter must not recover an execution identity")
}
func (*recreationAdmissionSource) PreviewRecreateSandboxes(context.Context, integration.SandboxRecreateRequest) (integration.SandboxRecreatePlan, error) {
	panic("adapter must not plan twice")
}
func (p *recreationAdmissionSource) PrepareDiagnosticRecreation(ctx context.Context, id, path string) (protocol.DiagnosticRecreationPreview, integration.SandboxRecreateRequest, error) {
	p.prepares++
	if p.prepareHook != nil {
		p.prepareHook(ctx)
	}
	observed, state := true, "running"
	if p.status == "skipped" {
		observed, state = false, "unknown"
	}
	return protocol.DiagnosticRecreationPreview{ID: id, PreviewID: "recreation-preview:" + strings.Repeat("b", 64), Status: p.status, PreviousState: state, RuntimeIdentityObserved: observed, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "fresh_bounded_registration", AgentOwnerCount: &p.owners, LeaseEvidence: p.lease}, integration.SandboxRecreateRequest{WorkspaceRoot: p.root, Workspace: path, ExpectedPlanID: strings.Repeat("a", 64)}, nil
}
func (p *recreationAdmissionSource) RecreateSandboxes(ctx context.Context, _ *slog.Logger, req integration.SandboxRecreateRequest) (integration.SandboxRecreateResult, error) {
	p.executes++
	p.captured = req
	if p.executeHook != nil {
		return p.executeHook(ctx)
	}
	for _, g := range p.groups {
		if g.Path == req.Workspace {
			return integration.SandboxRecreateResult{OK: true, Recreated: 1, Targets: []integration.SandboxRecreateTarget{{WorkspaceID: g.ID, WorkspacePath: g.Path, SandboxName: "PRIVATE-sandbox", SandboxID: "PRIVATE-runtime", Status: "recreated", Reason: "PRIVATE-message"}}}, nil
		}
	}
	return integration.SandboxRecreateResult{}, errors.New("PRIVATE-executor-error")
}
func recreationAdmissionFixture(t *testing.T) (*Server, *recreationAdmissionSource, protocol.DiagnosticMaintenanceRequest, *bool) {
	t.Helper()
	root := t.TempDir()
	origin, feedback := filepath.Join(root, "origin"), filepath.Join(root, "feedback")
	p := &recreationAdmissionSource{root: root, status: "recreate", lease: "unknown_no_lock_file", groups: []protocol.DiagnosticWorkspace{{ID: "origin", Path: origin, Registered: true, SandboxName: "PRIVATE-sandbox"}, {ID: "feedback", Path: feedback, Registered: true, Feedback: true, FeedbackOrigin: origin}}}
	s, _ := refreshFixture(t, p)
	pending := false
	s.SetResourcePublication(func() { pending = true }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: pending} })
	return s, p, protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "recreate", Mode: "preview", Scope: "origin", ParentID: "origin", Workspace: origin, FeedbackWorkspace: feedback}, &pending
}
func recreationAdmissionPreview(t *testing.T, s *Server, req protocol.DiagnosticMaintenanceRequest) diagnosticRecreationView {
	t.Helper()
	v, err := s.diagnosticRecreation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func recreationAdmissionApply(req protocol.DiagnosticMaintenanceRequest, v diagnosticRecreationView) protocol.DiagnosticMaintenanceRequest {
	req.Mode, req.PlanID = "apply", v.planID
	return req
}

func TestDiagnosticRecreationAdmissionExactNativeOneUse(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		t.Run(scope, func(t *testing.T) {
			s, p, req, pending := recreationAdmissionFixture(t)
			req.Scope = scope
			if scope == "feedback" {
				req.ParentID = "feedback"
				req.Workspace = req.FeedbackWorkspace
			}
			if scope == "installation" {
				req.Workspace = ""
			}
			v := recreationAdmissionPreview(t, s, req)
			if p.prepares != 1 || p.executes != 0 || len(s.recreationPlans) != 1 || !hexPlanID(v.planID) || v.planID == strings.Repeat("a", 64) || v.planID == v.proposal.PreviewID || v.mode != "preview" || v.outcome != "not_attempted" || *pending {
				t.Fatal("invalid nonexecuting preview")
			}
			expires, err := time.Parse(time.RFC3339Nano, v.expiresAt)
			if err != nil || !expires.After(time.Now()) || expires.After(time.Now().Add(time.Minute)) {
				t.Fatal("bad expiry")
			}
			wantScopeID := req.ParentID
			if scope == "installation" {
				wantScopeID = ""
			}
			if v.scopeID != wantScopeID || v.feedbackID != "feedback" {
				t.Fatal("wrong disclosed scope")
			}
			// Neither borrowed provider memory nor a returned pointer may change retained disclosure.
			*v.proposal.AgentOwnerCount = 999
			p.owners = 3
			if *s.recreationPlans[req.ParentID].view.proposal.AgentOwnerCount != 0 {
				t.Fatal("retained borrowed disclosure")
			}
			p.executeHook = func(ctx context.Context) (integration.SandboxRecreateResult, error) {
				_, _, ops, _ := s.store.OperationInspectionSnapshot()
				if len(ops) != 1 || ops[0].Status != "running" || ops[0].Kind != "workspace" || ops[0].WorkspaceID != req.ParentID {
					t.Fatal("missing exact native managed operation")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 30*time.Second {
					t.Fatal("missing cooperative budget")
				}
				return integration.SandboxRecreateResult{OK: true, Recreated: 1, Targets: []integration.SandboxRecreateTarget{{WorkspaceID: req.ParentID, WorkspacePath: p.captured.Workspace, Status: "recreated"}}}, nil
			}
			result, err := s.diagnosticRecreation(context.Background(), recreationAdmissionApply(req, v))
			if err != nil || result.mode != "apply" || result.problem != "none" || result.outcome != "native_restoration_checks_returned" || !result.resourceRefreshPending || !*pending || p.prepares != 1 || p.executes != 1 || len(s.recreationPlans) != 0 {
				t.Fatalf("not exact native apply: %+v %v", result, err)
			}
			expectedPath := p.groups[0].Path
			if scope == "feedback" {
				expectedPath = p.groups[1].Path
			}
			if p.captured != (integration.SandboxRecreateRequest{WorkspaceRoot: p.root, Workspace: expectedPath, ExpectedPlanID: strings.Repeat("a", 64)}) {
				t.Fatal("native identity/path rewritten")
			}
			_, _, ops, _ := s.store.OperationInspectionSnapshot()
			if len(ops) != 1 || ops[0].Status != "succeeded" || ops[0].CompletedTargets != 1 || ops[0].TotalTargets != 1 {
				t.Fatal("wrong returned operation")
			}
			if _, err := s.diagnosticRecreation(context.Background(), recreationAdmissionApply(req, v)); err == nil || p.executes != 1 {
				t.Fatal("replayed one-use plan")
			}
			raw, err := json.Marshal(result)
			if err != nil || string(raw) != "{}" {
				t.Fatal("in-process stage accidentally serializes a transport")
			}
		})
	}
}

func TestDiagnosticRecreationAdmissionConsumesFailedBindings(t *testing.T) {
	for _, kind := range []string{"expiry", "revision", "pending", "registration", "origin", "feedback", "scope", "target", "cancel_registration", "cancel_publication", "missing_capability", "blocked", "skipped"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req, pending := recreationAdmissionFixture(t)
			if kind == "blocked" || kind == "skipped" {
				p.status = kind
			}
			v := recreationAdmissionPreview(t, s, req)
			apply := recreationAdmissionApply(req, v)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "expiry":
				plan := s.recreationPlans[req.ParentID]
				plan.expires = time.Now().Add(-time.Second)
				s.recreationPlans[req.ParentID] = plan
			case "revision":
				s.store.ReportResourceMutation(protocol.ResourceMutation{})
			case "pending":
				*pending = true
			case "registration":
				p.groups[0].SandboxName = "PRIVATE-changed"
			case "origin":
				p.groups[1].FeedbackOrigin = filepath.Join(p.root, "other")
			case "feedback":
				apply.FeedbackWorkspace = filepath.Join(p.root, "other")
			case "scope":
				apply.Scope = "installation"
				apply.Workspace = ""
			case "target":
				apply.ParentID = "feedback"
			case "cancel_registration":
				p.registrationHook = func(int) { cancel() }
			case "cancel_publication":
				s.resourceSnapshot = func() protocol.Response { cancel(); return protocol.Response{} }
			case "missing_capability":
				s.integrations = integration.NewRegistry()
			}
			_, _, before, _ := s.store.OperationInspectionSnapshot()
			if _, err := s.diagnosticRecreation(ctx, apply); err == nil || strings.Contains(err.Error(), "PRIVATE") || p.executes != 0 || p.prepares != 1 || len(s.recreationPlans) != 0 {
				t.Fatalf("unsafe acceptance/retention: %v prepares=%d calls=%d", err, p.prepares, p.executes)
			}
			_, _, after, _ := s.store.OperationInspectionSnapshot()
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected apply created an operation")
			}
			*pending = false
			p.registrationHook = nil
			s.resourceSnapshot = func() protocol.Response { return protocol.Response{} }
			if _, err := s.diagnosticRecreation(context.Background(), recreationAdmissionApply(req, v)); err == nil || p.executes != 0 {
				t.Fatal("failed binding became reusable")
			}
		})
	}
}

func TestDiagnosticRecreationAdmissionPreservesPlanOnRejectedPreview(t *testing.T) {
	for _, kind := range []string{"prepare_cancel", "registration_cancel", "registration_drift", "publication_cancel", "publication_revision", "publication_pending", "busy", "refresh_running", "pre_cancel"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req, pending := recreationAdmissionFixture(t)
			v := recreationAdmissionPreview(t, s, req)
			old := s.recreationPlans[req.ParentID]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "prepare_cancel":
				p.prepareHook = func(context.Context) { cancel() }
			case "registration_cancel":
				p.registrationHook = func(int) { cancel() }
			case "registration_drift":
				p.prepareHook = func(context.Context) { p.groups[0].SandboxName = "PRIVATE-drift" }
			case "publication_cancel", "publication_revision", "publication_pending":
				calls := 0
				s.resourceSnapshot = func() protocol.Response {
					calls++
					if calls == 3 {
						switch kind {
						case "publication_cancel":
							cancel()
						case "publication_revision":
							s.store.ReportResourceMutation(protocol.ResourceMutation{})
						case "publication_pending":
							*pending = true
						}
					}
					return protocol.Response{ResourceRefreshPending: *pending}
				}
			case "busy":
				s.maintenanceMu.Lock()
				defer s.maintenanceMu.Unlock()
			case "refresh_running":
				s.refreshPlan = &diagnosticRefreshPlan{view: protocol.DiagnosticMaintenance{Status: "running"}}
			case "pre_cancel":
				cancel()
			}
			if _, err := s.diagnosticRecreation(ctx, req); err == nil || p.executes != 0 || len(s.recreationPlans) != 1 || !reflect.DeepEqual(old, s.recreationPlans[req.ParentID]) || s.recreationPlans[req.ParentID].view.planID != v.planID {
				t.Fatal("rejected preview superseded existing authority", err)
			}
		})
	}
}

func TestDiagnosticRecreationAdmissionSupersessionAndCapacity(t *testing.T) {
	s, p, req, _ := recreationAdmissionFixture(t)
	for i := 0; i < maxDiagnosticRecreationPlans; i++ {
		p.groups = append(p.groups, protocol.DiagnosticWorkspace{ID: fmt.Sprintf("workspace-%d", i), Path: filepath.Join(p.root, fmt.Sprintf("workspace-%d", i)), Registered: true})
	}
	req.Scope, req.Workspace = "installation", ""
	first := recreationAdmissionPreview(t, s, req)
	latest := recreationAdmissionPreview(t, s, req)
	if first.planID == latest.planID {
		t.Fatal("preview identity reused")
	}
	if _, err := s.diagnosticRecreation(context.Background(), recreationAdmissionApply(req, first)); err == nil || len(s.recreationPlans) != 1 {
		t.Fatal("superseded token consumed latest")
	}
	for i := 0; i < maxDiagnosticRecreationPlans-1; i++ {
		req.ParentID = fmt.Sprintf("workspace-%d", i)
		recreationAdmissionPreview(t, s, req)
	}
	req.ParentID = fmt.Sprintf("workspace-%d", maxDiagnosticRecreationPlans-1)
	if _, err := s.diagnosticRecreation(context.Background(), req); err == nil || len(s.recreationPlans) != maxDiagnosticRecreationPlans {
		t.Fatal("cache unbounded")
	}
	plan := s.recreationPlans["origin"]
	plan.expires = time.Now().Add(-time.Second)
	s.recreationPlans["origin"] = plan
	recreationAdmissionPreview(t, s, req)
	if len(s.recreationPlans) != maxDiagnosticRecreationPlans {
		t.Fatal("expired capacity not recovered")
	}
	if p.executes != 0 {
		t.Fatal("preview/cache operation executed")
	}
}

func TestDiagnosticRecreationAdmissionNativeErrorsRemainUnknown(t *testing.T) {
	for _, kind := range []string{"native_error", "cancelled", "empty", "wrong_target", "wrong_path", "false_ok", "inconsistent_counts"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req, pending := recreationAdmissionFixture(t)
			v := recreationAdmissionPreview(t, s, req)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.executeHook = func(context.Context) (integration.SandboxRecreateResult, error) {
				result := integration.SandboxRecreateResult{OK: true, Recreated: 1, Targets: []integration.SandboxRecreateTarget{{WorkspaceID: req.ParentID, WorkspacePath: req.Workspace, Status: "recreated", Reason: "PRIVATE-message"}}}
				switch kind {
				case "native_error":
					return result, errors.New("PRIVATE-native-error")
				case "cancelled":
					cancel()
				case "empty":
					return integration.SandboxRecreateResult{}, nil
				case "wrong_target":
					result.Targets[0].WorkspaceID = "other"
				case "wrong_path":
					result.Targets[0].WorkspacePath = "PRIVATE-other"
				case "false_ok":
					result.OK = false
				case "inconsistent_counts":
					result.Failed = 1
				}
				return result, nil
			}
			result, err := s.diagnosticRecreation(ctx, recreationAdmissionApply(req, v))
			if err != nil || result.problem == "none" || result.outcome != "unverified_may_be_partial" || !result.resourceRefreshPending || !*pending || p.executes != 1 || len(s.recreationPlans) != 0 || strings.Contains(result.problem, "PRIVATE") {
				t.Fatalf("unknown result overstated: %+v %v", result, err)
			}
			_, _, ops, _ := s.store.OperationInspectionSnapshot()
			if len(ops) != 1 || ops[0].Status != "failed" || ops[0].CompletedTargets != 0 {
				t.Fatal("unknown result recorded success")
			}
			if _, err := s.diagnosticRecreation(context.Background(), recreationAdmissionApply(req, v)); err == nil || p.executes != 1 {
				t.Fatal("partial/unknown result replayed")
			}
		})
	}
}

func TestDiagnosticRecreationAdmissionObservationsDoNotInventRemovalPolicy(t *testing.T) {
	for _, lease := range []string{"unknown_no_lock_file", "exclusive_available_at_probe", "contended_at_probe", "unavailable"} {
		t.Run(lease, func(t *testing.T) {
			s, p, req, _ := recreationAdmissionFixture(t)
			p.owners = 2
			p.lease = lease
			v := recreationAdmissionPreview(t, s, req)
			result, err := s.diagnosticRecreation(context.Background(), recreationAdmissionApply(req, v))
			if err != nil || result.problem != "none" || p.executes != 1 {
				t.Fatal("registered owners/deletion probe replaced native recreation writer policy", err)
			}
		})
	}
}

func TestDiagnosticRecreationAdmissionInvalidRequestsAndNoPublicRoute(t *testing.T) {
	s, p, req, _ := recreationAdmissionFixture(t)
	for name, mutate := range map[string]func(*protocol.DiagnosticMaintenanceRequest){"schema": func(q *protocol.DiagnosticMaintenanceRequest) { q.Schema-- }, "operation": func(q *protocol.DiagnosticMaintenanceRequest) { q.Operation = "cleanup" }, "parent": func(q *protocol.DiagnosticMaintenanceRequest) { q.ParentID = "" }, "oversized_parent": func(q *protocol.DiagnosticMaintenanceRequest) { q.ParentID = strings.Repeat("x", 513) }, "feedback": func(q *protocol.DiagnosticMaintenanceRequest) { q.FeedbackWorkspace = "relative" }, "scope": func(q *protocol.DiagnosticMaintenanceRequest) { q.Scope = "all" }, "workspace": func(q *protocol.DiagnosticMaintenanceRequest) { q.Workspace = "relative" }, "preview_identity": func(q *protocol.DiagnosticMaintenanceRequest) { q.PlanID = strings.Repeat("a", 64) }, "apply_identity": func(q *protocol.DiagnosticMaintenanceRequest) { q.Mode = "apply" }, "mode": func(q *protocol.DiagnosticMaintenanceRequest) { q.Mode = "force" }} {
		t.Run(name, func(t *testing.T) {
			q := req
			mutate(&q)
			if _, err := s.diagnosticRecreation(context.Background(), q); err == nil || p.registrations != 0 || p.prepares != 0 || p.executes != 0 {
				t.Fatal("invalid request reached native capability")
			}
		})
	}
	// The staged in-process backend must not accidentally advertise/enable apply.
	response := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &req})
	if response.OK || p.prepares != 0 || p.executes != 0 || len(s.recreationPlans) != 0 {
		t.Fatal("unqualified wire route enabled")
	}
}

func TestDiagnosticRecreationAdmissionFreezesBeforeCallbacks(t *testing.T) {
	s, p, req, _ := recreationAdmissionFixture(t)
	p.registrationHook = func(n int) {
		if n == 2 {
			p.owners = 1025
		}
	}
	v := recreationAdmissionPreview(t, s, req)
	if *v.proposal.AgentOwnerCount != 0 || *s.recreationPlans[req.ParentID].view.proposal.AgentOwnerCount != 0 || !protocol.ValidDiagnosticRecreationPreview(v.proposal, req.ParentID) {
		t.Fatal("post-capture callback rewrote the disclosed proposal")
	}
}

func TestDiagnosticRecreationAdmissionRejectsAmbiguousOrUnboundedRegistration(t *testing.T) {
	for _, kind := range []string{"duplicate_id", "duplicate_path", "relative_path", "unclean_path", "oversized_path", "oversized_id", "unregistered", "missing_feedback", "missing_origin", "missing_parent", "many_groups", "oversized_snapshot", "relative_native_root", "oversized_native_root", "unknown_native_status", "invalid_owners", "invalid_lease", "publication_missing", "invalidation_missing", "ambiguous_provider"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req, _ := recreationAdmissionFixture(t)
			switch kind {
			case "duplicate_id":
				p.groups[1].ID = p.groups[0].ID
			case "duplicate_path":
				p.groups[1].Path = p.groups[0].Path
			case "relative_path":
				p.groups[0].Path = "relative"
			case "unclean_path":
				p.groups[0].Path += "/../origin"
			case "oversized_path":
				p.groups[0].Path = "/" + strings.Repeat("x", 4096)
			case "oversized_id":
				p.groups[0].ID = strings.Repeat("x", 513)
			case "unregistered":
				p.groups[0].Registered = false
			case "missing_feedback":
				p.groups[1].Feedback = false
			case "missing_origin":
				p.groups[1].FeedbackOrigin = ""
			case "missing_parent":
				req.ParentID = "missing"
			case "many_groups":
				p.groups = append(p.groups, make([]protocol.DiagnosticWorkspace, 1024)...)
			case "oversized_snapshot":
				p.groups[0].NotePath = strings.Repeat("PRIVATE", 200000)
			case "relative_native_root":
				p.root = "relative"
			case "oversized_native_root":
				p.root = "/" + strings.Repeat("x", 4096)
			case "unknown_native_status":
				p.status = "healthy"
			case "invalid_owners":
				p.owners = 1025
			case "invalid_lease":
				p.lease = "PRIVATE-holder"
			case "publication_missing":
				s.resourceSnapshot = nil
			case "invalidation_missing":
				s.markResourcesPending = nil
			case "ambiguous_provider":
				s.integrations = integration.NewRegistry(p, p)
			}
			if _, err := s.diagnosticRecreation(context.Background(), req); err == nil || strings.Contains(err.Error(), "PRIVATE") || len(s.recreationPlans) != 0 || p.executes != 0 {
				t.Fatal("invalid scope/native capture retained authority", err)
			}
		})
	}
}
