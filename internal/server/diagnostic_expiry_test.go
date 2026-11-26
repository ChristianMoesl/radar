package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"radar/internal/agentservice"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/taskservice"
	"radar/internal/workspacegc"
)

type expiryAdapterSource struct {
	workspace.Source
	root                    string
	groups                  []protocol.DiagnosticWorkspace
	previews, calls, guards int
	seen                    []protocol.CleanupTarget
	previewHook, guardHook  func()
	fail                    bool
	targetsHook             func([]protocol.CleanupTarget) []protocol.CleanupTarget
}

func (p *expiryAdapterSource) DefaultRoot() (string, error) { return p.root, nil }
func (p *expiryAdapterSource) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	return append([]protocol.DiagnosticWorkspace{}, p.groups...), nil
}
func (p *expiryAdapterSource) PreviewCleanup(_ context.Context, q integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if q.Mode != integration.CleanupExpired {
		panic("expiry substituted manual or retirement mode")
	}
	p.previews++
	if p.previewHook != nil {
		p.previewHook()
	}
	var targets []protocol.CleanupTarget
	for _, r := range q.Task.SourceRefs {
		if r.Source == "workspace" {
			targets = append(targets, protocol.CleanupTarget{Source: "workspace", Kind: "workspace", SourceRefID: r.ID, ResourceID: r.WorkspaceID, WorkspaceID: r.WorkspaceID, Path: r.Path, ProvidesWorkspace: true, Operation: map[string]string{"private": "original"}})
		}
	}
	if p.targetsHook != nil {
		targets = p.targetsHook(targets)
	}
	return targets, nil
}
func (p *expiryAdapterSource) AdmitCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	return p, func() {}, nil
}
func (p *expiryAdapterSource) PreflightCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error {
	return nil
}
func (p *expiryAdapterSource) Cleanup(_ context.Context, q integration.CleanupRequest) (protocol.CleanupTarget, error) {
	if q.Mode != integration.CleanupExpired {
		panic("wrong native mode")
	}
	p.calls++
	p.seen = append(p.seen, q.Target)
	if p.fail {
		return q.Target, errors.New("PRIVATE native failure")
	}
	return q.Target, nil
}
func expiryAdapterFixture(t *testing.T) (*Server, *expiryAdapterSource, protocol.DiagnosticMaintenanceRequest, *bool) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	p := &expiryAdapterSource{root: root}
	for _, id := range []string{"origin", "feedback", "other"} {
		path := filepath.Join(root, id)
		if e := os.Mkdir(path, 0700); e != nil {
			t.Fatal(e)
		}
		p.groups = append(p.groups, protocol.DiagnosticWorkspace{ID: id, Path: path, Registered: true, Feedback: id == "feedback", FeedbackOrigin: filepath.Join(root, "origin")})
	}
	s, store := refreshFixture(t, p)
	pending := false
	s.SetResourcePublication(func() { pending = true }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: pending} })
	refs := []protocol.SourceRef{{ID: "item:ABC-123", EntityID: "item:ABC-123", Source: "test", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", Metadata: map[string]string{"completed_at": time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)}, LinkingKeys: []string{"mark:ABC-123"}}}
	for _, g := range p.groups {
		refs = append(refs, protocol.SourceRef{ID: "workspace:" + g.ID, EntityID: "workspace:" + g.ID, Source: "workspace", Kind: "workspace", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace, WorkspaceID: g.ID, Path: g.Path, ProvidesWorkspace: true, WorkspaceEntry: true, LinkingKeys: []string{"mark:ABC-123"}})
	}
	store.SetTasks([]protocol.Task{{Title: "PRIVATE title", Attention: "done", SourceRefs: refs}})
	s.cleanupService = cleanup.New(s.integrations.CleanupProviders())
	tasks := taskservice.New(store, s.logger, s.integrations)
	s.SetDiagnosticGCGuard(func(ctx context.Context, task protocol.Task, execute func() error) error {
		p.guards++
		if p.guardHook != nil {
			p.guardHook()
		}
		return tasks.GuardCleanup(ctx, task, execute)
	})
	return s, p, protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "expire", Mode: "preview", Scope: "origin", ParentID: "origin", Workspace: p.groups[0].Path, FeedbackWorkspace: p.groups[1].Path}, &pending
}
func TestDiagnosticExpiryAdapterExactScopeOneUseAndImmutableBundle(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		t.Run(scope, func(t *testing.T) {
			s, p, q, pending := expiryAdapterFixture(t)
			q.Scope = scope
			if scope == "feedback" {
				q.ParentID = "feedback"
				q.Workspace = q.FeedbackWorkspace
			}
			if scope == "installation" {
				q.Workspace = ""
				q.ParentID = "other"
			}
			response := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
			v := response.DiagnosticExpiry
			if !response.OK || v == nil || !protocol.ValidDiagnosticExpiry(*v, q) || p.previews != 1 || p.calls != 0 || p.guards != 0 || *pending || len(s.cleanupPlans) != 1 {
				t.Fatalf("native preview: %+v previews=%d guards=%d calls=%d", response, p.previews, p.guards, p.calls)
			}
			original := v.Proposal.Targets[0].ID
			v.Proposal.Targets[0].ID = "PRIVATE replacement"
			v.Proposal.Preserved[0] = "PRIVATE replacement"
			*v.Proposal.AgentOwnerCount = 99
			q.Mode, q.PlanID = "apply", v.PlanID
			result := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
			applied := result.DiagnosticExpiry
			if !result.OK || applied == nil || !protocol.ValidDiagnosticExpiry(*applied, q) || applied.Outcome != "native_bundle_returned_unverified" || applied.Proposal.Targets[0].ID != original || p.previews != 1 || p.calls != 1 || p.guards != 1 || !*pending || len(s.cleanupPlans) != 0 || p.seen[0].WorkspaceID != q.ParentID || p.seen[0].Operation["private"] != "original" {
				t.Fatalf("native apply: %+v previews=%d guards=%d calls=%d", result, p.previews, p.guards, p.calls)
			}
			if replay := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q}); replay.OK || p.calls != 1 {
				t.Fatal("consumed plan replayed")
			}
		})
	}
}
func TestDiagnosticExpiryAdapterRefusesDriftAndNeverRestoresConsumedPlan(t *testing.T) {
	for _, kind := range []string{"registration", "pending", "revision", "wrong-target", "wrong-scope", "expired", "cancelled", "lifecycle-drift", "native-error"} {
		t.Run(kind, func(t *testing.T) {
			s, p, q, pending := expiryAdapterFixture(t)
			v, e := s.diagnosticExpiry(context.Background(), q)
			if e != nil {
				t.Fatal(e)
			}
			q.Mode, q.PlanID = "apply", v.PlanID
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "registration":
				p.groups[0].SessionName = "PRIVATE replacement"
			case "pending":
				*pending = true
			case "revision":
				s.store.BeginOperation("workspace", 0, "")
			case "wrong-target":
				q.ParentID = "other"
			case "wrong-scope":
				q.Scope = "installation"
				q.Workspace = ""
			case "expired":
				plan := s.cleanupPlans[v.Proposal.TaskID]
				plan.expires = time.Now().Add(-time.Second)
				s.cleanupPlans[v.Proposal.TaskID] = plan
			case "cancelled":
				p.guardHook = cancel
			case "lifecycle-drift":
				p.guardHook = func() { p.groups[0].SessionName = "PRIVATE replacement" }
			case "native-error":
				p.fail = true
			}
			result, e := s.diagnosticExpiry(ctx, q)
			if kind == "native-error" {
				if e != nil || result.Outcome != "unverified_may_be_partial" || p.calls != 1 || !*pending {
					t.Fatal(result, e, p.calls)
				}
			} else if e == nil || p.calls != 0 {
				t.Fatal("changed/native plan dispatched", result, e, p.calls)
			}
			if len(s.cleanupPlans) != 0 {
				t.Fatal("refused apply restored plan")
			}
			q.ParentID, q.Scope, q.Workspace = "origin", "origin", p.groups[0].Path
			if _, e := s.diagnosticExpiry(context.Background(), q); e == nil {
				t.Fatal("refused plan replayed")
			}
		})
	}
}
func TestDiagnosticExpiryAdapterPreviewDriftCannotSupersedePrior(t *testing.T) {
	for _, kind := range []string{"cancelled", "revision", "registration"} {
		t.Run(kind, func(t *testing.T) {
			s, p, q, _ := expiryAdapterFixture(t)
			v, e := s.diagnosticExpiry(context.Background(), q)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.previewHook = func() {
				switch kind {
				case "cancelled":
					cancel()
				case "revision":
					s.store.BeginOperation("workspace", 0, "")
				case "registration":
					p.groups[0].SessionName = "PRIVATE replacement"
				}
			}
			if _, e := s.diagnosticExpiry(ctx, q); e == nil {
				t.Fatal("changed preview issued")
			}
			if p.calls != 0 || p.guards != 0 || s.cleanupPlans[v.Proposal.TaskID].id != v.PlanID {
				t.Fatal("failed preview superseded or executed")
			}
		})
	}
}
func TestDiagnosticExpiryAdapterMissingGuardFailsClosed(t *testing.T) {
	s, p, q, _ := expiryAdapterFixture(t)
	s.gcGuard = nil
	if _, e := s.diagnosticExpiry(context.Background(), q); e == nil || p.previews != 0 || p.calls != 0 || len(s.cleanupPlans) != 0 {
		t.Fatal("missing lifecycle adapter permitted preview")
	}
	if !strings.Contains(q.Operation, "expire") {
		t.Fatal("fixture")
	}
}

type expiryRuntimeProvider struct {
	fakeCleanupSource
	owner *expiryAdapterSource
	order *[]string
	fail  bool
}

func (p expiryRuntimeProvider) PreviewCleanup(_ context.Context, q integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if q.Mode != integration.CleanupExpired {
		panic("wrong runtime mode")
	}
	var targets []protocol.CleanupTarget
	for _, r := range q.Task.SourceRefs {
		if r.Source != "workspace" {
			continue
		}
		for _, g := range p.owner.groups {
			if g.ID != r.WorkspaceID {
				continue
			}
			kind, resource := "session", g.SessionName
			if p.name == "sbx" {
				kind, resource = "sandbox", g.SandboxName
			}
			if resource != "" {
				targets = append(targets, protocol.CleanupTarget{Source: p.name, Kind: kind, SourceRefID: p.name + ":" + g.ID, ResourceID: resource, WorkspaceID: g.ID, Path: g.Path, Operation: map[string]string{"private": "PRIVATE retained runtime"}})
			}
		}
	}
	return targets, nil
}
func (p expiryRuntimeProvider) AdmitCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	return p, func() {}, nil
}
func (p expiryRuntimeProvider) PreflightCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error {
	return nil
}
func (p expiryRuntimeProvider) Cleanup(_ context.Context, q integration.CleanupRequest) (protocol.CleanupTarget, error) {
	if q.Mode != integration.CleanupExpired || q.Target.WorkspaceID != "origin" || q.Target.Operation["private"] != "PRIVATE retained runtime" {
		panic("native target changed")
	}
	*p.order = append(*p.order, p.name)
	if p.fail {
		return q.Target, errors.New("PRIVATE partial runtime error")
	}
	return q.Target, nil
}
func TestDiagnosticExpiryAdapterOrderedWholeRuntimeBundleAndPartialPrefix(t *testing.T) {
	for _, fail := range []string{"none", "tmux", "sbx"} {
		t.Run(fail, func(t *testing.T) {
			s, p, q, pending := expiryAdapterFixture(t)
			p.groups[0].SessionName = "PRIVATE terminal"
			p.groups[0].SandboxName = "PRIVATE sandbox"
			order := []string{}
			terminal := expiryRuntimeProvider{fakeCleanupSource: fakeCleanupSource{name: "tmux"}, owner: p, order: &order, fail: fail == "tmux"}
			sandbox := expiryRuntimeProvider{fakeCleanupSource: fakeCleanupSource{name: "sbx"}, owner: p, order: &order, fail: fail == "sbx"}
			s.integrations = integration.NewRegistry(terminal, sandbox, p)
			s.cleanupService = cleanup.New(s.integrations.CleanupProviders())
			v, e := s.diagnosticExpiry(context.Background(), q)
			if e != nil || len(v.Proposal.Targets) != 3 {
				t.Fatal(v, e)
			}
			raw, _ := json.Marshal(v)
			if strings.Contains(string(raw), "PRIVATE") {
				t.Fatal("private runtime disclosure")
			}
			for i, want := range []string{"tmux", "sbx", "workspace"} {
				if v.Proposal.Targets[i].Provider != want {
					t.Fatal("native order changed")
				}
			}
			q.Mode, q.PlanID = "apply", v.PlanID
			result, e := s.diagnosticExpiry(context.Background(), q)
			n := 3
			stage := "completed"
			wantCalls := 2
			wantWorkspace := 1
			if fail == "tmux" {
				n = 0
				stage = "provider_call"
				wantCalls = 1
				wantWorkspace = 0
			}
			if fail == "sbx" {
				n = 1
				stage = "provider_call"
				wantWorkspace = 0
			}
			if e != nil || !protocol.ValidDiagnosticExpiry(result, q) || result.CompletedTargets == nil || *result.CompletedTargets != n || len(order) != wantCalls || p.calls != wantWorkspace || result.Execution == nil || result.Execution.Stage != stage || !*pending {
				t.Fatal(result, e, order, p.calls)
			}
			if _, e := s.diagnosticExpiry(context.Background(), q); e == nil {
				t.Fatal("partial native bundle replayed")
			}
		})
	}
}
func TestDiagnosticExpiryAdapterOwnerAndLifecycleAdmissionRefusals(t *testing.T) {
	for _, kind := range []string{"stopped_owner", "malformed_owner", "lifecycle_denied", "expired_during_guard"} {
		t.Run(kind, func(t *testing.T) {
			s, p, q, pending := expiryAdapterFixture(t)
			if kind == "lifecycle_denied" {
				s.SetDiagnosticGCGuard(func(context.Context, protocol.Task, func() error) error { return errors.New("PRIVATE denial") })
			}
			if kind == "stopped_owner" || kind == "malformed_owner" {
				dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "owner")
				if e := os.MkdirAll(dir, 0700); e != nil {
					t.Fatal(e)
				}
				raw := []byte("PRIVATE invalid registration")
				if kind == "stopped_owner" {
					raw, _ = json.Marshal(agentservice.Registration{Version: 1, ID: "owner", Desired: false, Workspace: protocol.AgentWorkspaceBinding{ID: "origin", Path: p.groups[0].Path, Mode: "local"}})
				}
				if e := os.WriteFile(filepath.Join(dir, "registration.json"), raw, 0600); e != nil {
					t.Fatal(e)
				}
				if _, e := s.diagnosticExpiry(context.Background(), q); e == nil || len(s.cleanupPlans) != 0 || p.calls != 0 || p.guards != 0 || *pending {
					t.Fatal("unknown/stopped owner allowed plan")
				}
				return
			}
			v, e := s.diagnosticExpiry(context.Background(), q)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "expired_during_guard" {
				retained := s.cleanupPlans[v.Proposal.TaskID]
				p.guardHook = func() { retained.expiry.admission.expires = time.Now().Add(-time.Second) }
			}
			q.Mode, q.PlanID = "apply", v.PlanID
			if _, e := s.diagnosticExpiry(context.Background(), q); e == nil || len(s.cleanupPlans) != 0 || p.calls != 0 || *pending {
				t.Fatal("denied lifecycle dispatched or restored plan")
			}
		})
	}
}
func TestDiagnosticExpiryAdapterLatestPerTaskPlanSupersedesAcrossWorkspaces(t *testing.T) {
	s, p, q, _ := expiryAdapterFixture(t)
	first, e := s.diagnosticExpiry(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	other := q
	other.Scope, other.ParentID, other.Workspace = "installation", "other", ""
	second, e := s.diagnosticExpiry(context.Background(), other)
	if e != nil || first.PlanID == second.PlanID || len(s.cleanupPlans) != 1 {
		t.Fatal(first, second, e)
	}
	q.Mode, q.PlanID = "apply", first.PlanID
	if _, e := s.diagnosticExpiry(context.Background(), q); e == nil || p.calls != 0 {
		t.Fatal("superseded plan executed")
	}
	if len(s.cleanupPlans) != 1 {
		t.Fatal("wrong token consumed newer plan")
	}
	other.Mode, other.PlanID = "apply", second.PlanID
	if _, e := s.diagnosticExpiry(context.Background(), other); e != nil || p.calls != 1 || p.seen[0].WorkspaceID != "other" {
		t.Fatal("latest exact workspace changed", e)
	}
}
func TestDiagnosticExpiryAdapterBoundsPrivateCandidateBeforeProviderPlanning(t *testing.T) {
	s, p, q, _ := expiryAdapterFixture(t)
	plan, err := workspacegc.BuildPlan(s.store, time.Now(), workspacegc.Options{WorkspaceRoot: p.root})
	if err != nil || len(plan.Candidates) != 3 {
		t.Fatal("fixture candidates", err, len(plan.Candidates))
	}
	task := plan.Candidates[0].Task
	task.Title = strings.Repeat("x", maxCleanupPlanBytes+1)
	s.store.SetTasks([]protocol.Task{task})
	if _, e := s.diagnosticExpiry(context.Background(), q); e == nil || p.previews != 0 || p.guards != 0 || p.calls != 0 || len(s.cleanupPlans) != 0 {
		t.Fatal("oversized private native candidate retained")
	}
}
