package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/integration"
	agentsource "radar/internal/integration/agent"
	"radar/internal/protocol"
)

type stopAdapterWorkspace struct {
	binding protocol.AgentWorkspaceBinding
}

func (w *stopAdapterWorkspace) AgentWorkspace(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
	return w.binding, nil
}
func (*stopAdapterWorkspace) PrepareAgentWorkspace(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
	panic("stop prepared a workspace")
}

type stopAdapterSource struct {
	*agentsource.Source
	prepares  int
	guardHook func()
	wrap      func(integration.AgentStopPlan) integration.AgentStopPlan
}

func (p *stopAdapterSource) PrepareDiagnosticAgentStop(ctx context.Context, id string, binding protocol.AgentWorkspaceBinding, guard integration.AgentStopGuard) (integration.AgentStopPlan, error) {
	p.prepares++
	native, err := p.Source.PrepareDiagnosticAgentStop(ctx, id, binding, func(c context.Context, execute func() error) error {
		if p.guardHook != nil {
			p.guardHook()
		}
		return guard(c, execute)
	})
	if err == nil && p.wrap != nil {
		native = p.wrap(native)
	}
	return native, err
}

type stopAdapterWrappedPlan struct {
	integration.AgentStopPlan
	expires time.Time
	bytes   int
	after   func()
	fail    bool
}

func (p stopAdapterWrappedPlan) ExpiresAt() time.Time { return p.expires }
func (p stopAdapterWrappedPlan) RetainedBytes() int   { return p.bytes }
func (p stopAdapterWrappedPlan) Execute(ctx context.Context) error {
	err := p.AgentStopPlan.Execute(ctx)
	if p.after != nil {
		p.after()
	}
	if p.fail {
		return errors.New("PRIVATE native failure after desired write")
	}
	return err
}

func stopAdapterFixture(t *testing.T, target string) (*Server, *manualAdapterSource, *stopAdapterSource, protocol.DiagnosticMaintenanceRequest, string, *bool) {
	t.Helper()
	s, groups, q := manualAdapterFixture(t)
	root, err := os.MkdirTemp("", "ds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
	binding := protocol.AgentWorkspaceBinding{ID: target, Mode: "local"}
	for _, g := range groups.groups {
		if g.ID == target {
			binding.Path = g.Path
		}
	}
	reg := agentservice.Registration{Version: 1, ID: "worker", Entrypoint: "PRIVATE entrypoint", Desired: true, Workspace: binding}
	dir := filepath.Join(root, "agents", reg.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reg)
	file := filepath.Join(dir, "registration.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	source := &stopAdapterSource{Source: agentsource.NewSource(&stopAdapterWorkspace{binding: binding})}
	s.integrations = integration.NewRegistry(groups, source)
	ref := protocol.SourceRef{ID: "agent:instance:worker", EntityID: "agent:instance:worker", Source: "agent", Kind: "agent", AgentID: "worker", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource, Metadata: map[string]string{"agent_id": "worker", "workspace_id": target, "workspace_path": binding.Path, "execution": "local", "private": "PRIVATE metadata"}}
	s.store.SetTasks([]protocol.Task{{Title: "PRIVATE task", SourceRefs: []protocol.SourceRef{ref}}})
	q.Operation = "agent_stop"
	for _, task := range s.store.Tasks() {
		for _, r := range task.SourceRefs {
			if r.AgentID == "worker" {
				q.ParentID = strconv.Itoa(task.ID)
			}
		}
	}
	pending := false
	s.SetResourcePublication(func() { pending = true }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: pending} })
	return s, groups, source, q, file, &pending
}
func stopDesired(t *testing.T, file string) bool {
	t.Helper()
	raw, err := os.ReadFile(file)
	var reg agentservice.Registration
	if err != nil || json.Unmarshal(raw, &reg) != nil {
		t.Fatal("registration removed/unreadable", err)
	}
	return reg.Desired
}
func stopApply(q protocol.DiagnosticMaintenanceRequest, v diagnosticAgentStopView) protocol.DiagnosticMaintenanceRequest {
	q.Mode, q.PlanID = "apply", v.planID
	return q
}

func TestDiagnosticAgentStopPrivateExactNativePlanAndOneUse(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		t.Run(scope, func(t *testing.T) {
			target := scope
			if scope == "installation" {
				target = "other"
			}
			s, _, provider, q, file, pending := stopAdapterFixture(t, target)
			q.Scope = scope
			if scope == "feedback" {
				q.Workspace = q.FeedbackWorkspace
			}
			if scope == "installation" {
				q.Workspace = ""
			}
			before := s.store.Revision()
			v, err := s.prepareDiagnosticAgentStop(context.Background(), q, "worker")
			if err != nil || provider.prepares != 1 || !v.desired || v.running || v.taskID < 1 || v.workspace != target || v.feedback != "feedback" || !hexPlanID(v.planID) || v.outcome != "native_exact_stop_plan_not_consent" || s.store.Revision() != before || *pending || !stopDesired(t, file) {
				t.Fatal("invalid/effectful capture", v, err)
			}
			plan := s.cleanupPlans[v.taskID]
			if plan.agentStop == nil || plan.payload != nil || plan.diagnostic != nil || !plan.expires.Equal(plan.agentStop.native.ExpiresAt()) || time.Until(plan.expires) > 30*time.Second {
				t.Fatal("second cache, fake cleanup, or extended native expiry")
			}
			for _, private := range []any{v, plan.agentStop, plan.agentStop.native} {
				raw, _ := json.Marshal(private)
				if string(raw) != "{}" {
					t.Fatal("private plan/view serialized", string(raw))
				}
			}
			v.agent, v.workspace = "other", "other" // returned value cannot replace retained target
			q = stopApply(q, v)
			applied, err := s.applyDiagnosticAgentStop(context.Background(), q, "worker")
			if err != nil || applied.agent != "worker" || applied.workspace != target || !applied.dispatched || applied.problem != "none" || applied.outcome != "native_handler_returned_unverified" || stopDesired(t, file) || !*pending || len(s.cleanupPlans) != 0 || provider.prepares != 1 {
				t.Fatal("native apply failed/overclaimed/replanned", applied, err)
			}
			var reg agentservice.Registration
			raw, _ := os.ReadFile(file)
			_ = json.Unmarshal(raw, &reg)
			if err := agentservice.CheckWorkspaceRemoval(reg.Workspace.Path); err == nil {
				t.Fatal("stopped registration released workspace ownership")
			}
			if _, err := s.applyDiagnosticAgentStop(context.Background(), q, "worker"); err == nil {
				t.Fatal("lost reply/success replayed")
			}
		})
	}
}

func TestDiagnosticAgentStopPrivateConsumesRefusedApply(t *testing.T) {
	for _, kind := range []string{"scope", "feedback", "parent", "agent", "operation", "mode", "schema", "expired", "revision", "pending", "cancelled", "hidden", "registration", "ref", "active-refresh"} {
		t.Run(kind, func(t *testing.T) {
			s, groups, _, q, file, pending := stopAdapterFixture(t, "origin")
			v, err := s.prepareDiagnosticAgentStop(context.Background(), q, "worker")
			if err != nil {
				t.Fatal(err)
			}
			q = stopApply(q, v)
			id := "worker"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "scope":
				q.Scope = "installation"
				q.Workspace = ""
			case "feedback":
				q.FeedbackWorkspace = q.Workspace
			case "parent":
				q.ParentID = "other"
			case "agent":
				id = "other"
			case "operation":
				q.Operation = "cleanup"
			case "mode":
				q.Mode = "preview"
			case "schema":
				q.Schema--
			case "expired":
				p := s.cleanupPlans[v.taskID]
				p.expires = time.Now().Add(-time.Second)
				s.cleanupPlans[v.taskID] = p
			case "revision":
				s.store.NotifyChange()
			case "pending":
				*pending = true
			case "cancelled":
				cancel()
			case "hidden":
				groups.hidden.Store(true)
			case "registration":
				groups.groups[0].SessionName = "PRIVATE changed"
			case "ref":
				groups.transform = func(tasks []protocol.Task) []protocol.Task {
					tasks[0].SourceRefs[0].Metadata = map[string]string{}
					return tasks
				}
			case "active-refresh":
				s.refreshPlan = &diagnosticRefreshPlan{view: protocol.DiagnosticMaintenance{Status: "running"}}
			}
			if _, err := s.applyDiagnosticAgentStop(ctx, q, id); err == nil || strings.Contains(err.Error(), "PRIVATE") || !stopDesired(t, file) || len(s.cleanupPlans) != 0 {
				t.Fatal("refused apply changed state or retained plan", err)
			}
		})
	}
}

func TestDiagnosticAgentStopPrivateFinalGuardRechecksScopeAfterNativeAdmission(t *testing.T) {
	for _, kind := range []string{"pending", "revision", "registration", "hidden", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			s, groups, provider, q, file, pending := stopAdapterFixture(t, "origin")
			v, err := s.prepareDiagnosticAgentStop(context.Background(), q, "worker")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			provider.guardHook = func() {
				calls++
				switch kind {
				case "pending":
					*pending = true
				case "revision":
					s.store.NotifyChange()
				case "registration":
					groups.groups[0].SessionName = "PRIVATE drift"
				case "hidden":
					groups.hidden.Store(true)
				case "cancelled":
					cancel()
				}
			}
			if _, err := s.applyDiagnosticAgentStop(ctx, stopApply(q, v), "worker"); err == nil || calls != 1 || !stopDesired(t, file) || len(s.cleanupPlans) != 0 {
				t.Fatal("scope drift reached first native effect", calls, err)
			}
		})
	}
}

func TestDiagnosticAgentStopPrivateRefusesWrongExactRefAndFeedbackScope(t *testing.T) {
	for _, kind := range []string{"other-agent-in-scoped-task", "duplicate", "id", "entity", "metadata-id", "workspace-id", "workspace-path", "mode", "hidden", "oversized", "unregistered-feedback", "origin-relation", "duplicate-provider"} {
		t.Run(kind, func(t *testing.T) {
			s, groups, provider, q, file, pending := stopAdapterFixture(t, "origin")
			switch kind {
			case "hidden":
				groups.hidden.Store(true)
			case "unregistered-feedback":
				groups.groups[1].Registered = false
			case "origin-relation":
				groups.groups[1].FeedbackOrigin = groups.groups[2].Path
			case "duplicate-provider":
				s.integrations = integration.NewRegistry(groups, provider, provider)
			default:
				groups.transform = func(tasks []protocol.Task) []protocol.Task {
					r := tasks[0].SourceRefs[0]
					m := map[string]string{}
					for k, v := range r.Metadata {
						m[k] = v
					}
					r.Metadata = m
					switch kind {
					case "other-agent-in-scoped-task":
						r.Metadata["workspace_id"], r.Metadata["workspace_path"] = "other", groups.groups[2].Path
						tasks[0].SourceRefs = append(tasks[0].SourceRefs, protocol.SourceRef{Path: q.Workspace})
					case "duplicate":
						tasks[0].SourceRefs = append(tasks[0].SourceRefs, r)
					case "id":
						r.ID = "other"
					case "entity":
						r.EntityID = "other"
					case "metadata-id":
						r.Metadata["agent_id"] = "other"
					case "workspace-id":
						r.Metadata["workspace_id"] = "other"
					case "workspace-path":
						r.Metadata["workspace_path"] = groups.groups[2].Path
					case "mode":
						r.Metadata["execution"] = "sbx"
					case "oversized":
						r.Metadata["private"] = strings.Repeat("x", 64*1024)
					}
					tasks[0].SourceRefs[0] = r
					return tasks
				}
			}
			if _, err := s.prepareDiagnosticAgentStop(context.Background(), q, "worker"); err == nil || provider.prepares != 0 || !stopDesired(t, file) || *pending || len(s.cleanupPlans) != 0 {
				t.Fatal("unsafe scope reached native capture", err, provider.prepares)
			}
		})
	}
}

func TestDiagnosticAgentStopPrivateRetentionBoundsPreservePreviousPreview(t *testing.T) {
	for _, kind := range []string{"zero-bytes", "oversized-native", "expired-native", "extended-native", "oversized-context"} {
		t.Run(kind, func(t *testing.T) {
			s, groups, provider, q, _, _ := stopAdapterFixture(t, "origin")
			v, err := s.prepareDiagnosticAgentStop(context.Background(), q, "worker")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "oversized-context" {
				groups.groups[0].NotePath = strings.Repeat("x", 300*1024)
			} else {
				provider.wrap = func(p integration.AgentStopPlan) integration.AgentStopPlan {
					wrapped := stopAdapterWrappedPlan{AgentStopPlan: p, expires: p.ExpiresAt(), bytes: p.RetainedBytes()}
					switch kind {
					case "zero-bytes":
						wrapped.bytes = 0
					case "oversized-native":
						wrapped.bytes = maxCleanupPlanBytes + 1
					case "expired-native":
						wrapped.expires = time.Now().Add(-time.Second)
					case "extended-native":
						wrapped.expires = time.Now().Add(time.Minute)
					}
					return wrapped
				}
			}
			if _, err := s.prepareDiagnosticAgentStop(context.Background(), q, "worker"); err == nil || len(s.cleanupPlans) != 1 || s.cleanupPlans[v.taskID].id != v.planID {
				t.Fatal("invalid retention replaced previous plan", err)
			}
		})
	}
}

func TestDiagnosticAgentStopPrivateSharedCacheSupersessionAndCapacity(t *testing.T) {
	s, _, _, q, _, _ := stopAdapterFixture(t, "origin")
	v, err := s.prepareDiagnosticAgentStop(context.Background(), q, "worker")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := protocol.CleanupPreview{TaskID: v.taskID, Targets: []protocol.CleanupTarget{{Source: "git", Kind: "worktree", ResourceID: "other"}}}
	issued, err := s.issueCleanupPlan(context.Background(), cleanup, v.revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.applyDiagnosticAgentStop(context.Background(), stopApply(q, v), "worker"); err == nil {
		t.Fatal("cleanup did not supersede stop")
	}
	v, err = s.prepareDiagnosticAgentStop(context.Background(), q, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.takeCleanupPlan(&issued); err == nil {
		t.Fatal("stop did not supersede cleanup")
	}
	for id := v.taskID + 1; len(s.cleanupPlans) < maxCleanupPlans; id++ {
		s.cleanupPlans[id] = nativeCleanupPlan{id: strings.Repeat("a", 64), revision: v.revision, expires: time.Now().Add(time.Minute)}
	}
	if _, err := s.issueCleanupPlan(context.Background(), protocol.CleanupPreview{TaskID: 999999, Targets: cleanup.Targets}, v.revision); err == nil || len(s.cleanupPlans) != maxCleanupPlans {
		t.Fatal("shared capacity bypassed")
	}
	latest, err := s.prepareDiagnosticAgentStop(context.Background(), q, "worker")
	if err != nil || latest.planID == v.planID || len(s.cleanupPlans) != maxCleanupPlans {
		t.Fatal("at-capacity same-task supersession failed", err)
	}
}

func TestDiagnosticAgentStopPrivateNativeErrorAndCancellationAreNotResourceOutcomes(t *testing.T) {
	for _, kind := range []string{"error", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			s, _, provider, q, file, pending := stopAdapterFixture(t, "origin")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider.wrap = func(p integration.AgentStopPlan) integration.AgentStopPlan {
				w := stopAdapterWrappedPlan{AgentStopPlan: p, expires: p.ExpiresAt(), bytes: p.RetainedBytes(), fail: kind == "error"}
				if kind == "cancelled" {
					w.after = cancel
				}
				return w
			}
			v, err := s.prepareDiagnosticAgentStop(context.Background(), q, "worker")
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.applyDiagnosticAgentStop(ctx, stopApply(q, v), "worker")
			if err != nil || result.problem == "none" || result.outcome != "unverified_may_be_partial" || !result.dispatched || !*pending || stopDesired(t, file) || len(s.cleanupPlans) != 0 {
				t.Fatal("native partial/cancelled return became success/rollback", result, err)
			}
		})
	}
}

func TestDiagnosticAgentStopPrivateNativeCannotExecuteBeforeConsumedApply(t *testing.T) {
	s, _, _, q, file, pending := stopAdapterFixture(t, "origin")
	v, err := s.prepareDiagnosticAgentStop(context.Background(), q, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupPlans[v.taskID].agentStop.native.Execute(context.Background()); err == nil || !stopDesired(t, file) || *pending {
		t.Fatal("detached/pre-apply native capture bypassed adapter")
	}
	if _, err := s.applyDiagnosticAgentStop(context.Background(), stopApply(q, v), "worker"); err == nil || len(s.cleanupPlans) != 0 {
		t.Fatal("preconsumed native capture replayed")
	}
}

func TestDiagnosticAgentStopPrivateHasNoPublicTransport(t *testing.T) {
	s, _, provider, q, file, pending := stopAdapterFixture(t, "origin")
	response := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
	if response.OK || provider.prepares != 0 || !stopDesired(t, file) || *pending || len(s.cleanupPlans) != 0 {
		t.Fatal("private stop unexpectedly enabled RPC", response)
	}
}
