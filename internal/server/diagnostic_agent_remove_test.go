package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/integration"
	agentsource "radar/internal/integration/agent"
	"radar/internal/protocol"
)

type removeAdapterWorkspace struct {
	binding protocol.AgentWorkspaceBinding
}

func (w *removeAdapterWorkspace) AgentWorkspace(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
	return w.binding, nil
}
func (*removeAdapterWorkspace) PrepareAgentWorkspace(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
	panic("removal prepared a workspace")
}

type removeAdapterSource struct {
	*agentsource.Source
	prepares  int
	guardHook func()
	wrap      func(integration.AgentRemovePlan) integration.AgentRemovePlan
}

func (p *removeAdapterSource) PrepareDiagnosticAgentRemove(ctx context.Context, id string, binding protocol.AgentWorkspaceBinding, guard integration.AgentRemoveGuard) (integration.AgentRemovePlan, error) {
	p.prepares++
	native, err := p.Source.PrepareDiagnosticAgentRemove(ctx, id, binding, func(c context.Context, execute func() error) error {
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

type removeAdapterWrappedPlan struct {
	integration.AgentRemovePlan
	expires time.Time
	bytes   int
	after   func()
	fail    bool
	preview *integration.AgentRemovePreview
}

func (p removeAdapterWrappedPlan) Preview() integration.AgentRemovePreview {
	if p.preview != nil {
		return *p.preview
	}
	return p.AgentRemovePlan.Preview()
}
func (p removeAdapterWrappedPlan) ExpiresAt() time.Time { return p.expires }
func (p removeAdapterWrappedPlan) RetainedBytes() int   { return p.bytes }
func (p removeAdapterWrappedPlan) Execute(ctx context.Context) error {
	err := p.AgentRemovePlan.Execute(ctx)
	if p.after != nil {
		p.after()
	}
	if p.fail {
		return errors.New("PRIVATE native failure after registration unlink")
	}
	return err
}

func removeAdapterFixture(t *testing.T, target string) (*Server, *manualAdapterSource, *removeAdapterSource, protocol.DiagnosticMaintenanceRequest, string, *bool) {
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
	reg := agentservice.Registration{Version: 1, ID: "worker", Entrypoint: "PRIVATE entrypoint", Desired: false, Workspace: binding}
	dir := filepath.Join(root, "agents", reg.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reg)
	file := filepath.Join(dir, "registration.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	schedules := []byte(`{"version":1,"schedules":[{"name":"morning","time":"09:00","weekdays":["mon"],"timezone":"UTC","payload":{"private":"payload"},"id":"schedule-one","enabled":true,"eligible_after":"2026-10-26T08:00:00Z"},{"name":"evening","time":"18:00","weekdays":["mon"],"timezone":"UTC","payload":{"private":"other"},"id":"schedule-two","enabled":false,"eligible_after":"2026-10-26T08:00:00Z"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "schedules.json"), schedules, 0600); err != nil {
		t.Fatal(err)
	}
	source := &removeAdapterSource{Source: agentsource.NewSource(&removeAdapterWorkspace{binding: binding})}
	s.integrations = integration.NewRegistry(groups, source)
	ref := protocol.SourceRef{ID: "agent:instance:worker", EntityID: "agent:instance:worker", Source: "agent", Kind: "agent", AgentID: "worker", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource, Metadata: map[string]string{"agent_id": "worker", "workspace_id": target, "workspace_path": binding.Path, "execution": "local", "private": "PRIVATE metadata"}}
	s.store.SetTasks([]protocol.Task{{Title: "PRIVATE task", SourceRefs: []protocol.SourceRef{ref}}})
	q.Operation, q.AgentID = "agent_remove", "worker"
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
func removeRegistered(t *testing.T, file string) bool {
	t.Helper()
	_, err := os.Lstat(file)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}
func removeApply(q protocol.DiagnosticMaintenanceRequest, v diagnosticAgentRemoveView) protocol.DiagnosticMaintenanceRequest {
	q.Mode, q.PlanID = "apply", v.planID
	return q
}

func TestDiagnosticAgentRemovePrivateExactNativePlanAndOneUse(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		t.Run(scope, func(t *testing.T) {
			target := scope
			if scope == "installation" {
				target = "other"
			}
			s, _, provider, q, file, pending := removeAdapterFixture(t, target)
			q.Scope = scope
			if scope == "feedback" {
				q.Workspace = q.FeedbackWorkspace
			}
			if scope == "installation" {
				q.Workspace = ""
			}
			before := s.store.Revision()
			v, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
			if err != nil || provider.prepares != 1 || v.schedules != 2 || v.enabledSchedules != 1 || v.taskID < 1 || v.workspace != target || v.feedback != "feedback" || !hexPlanID(v.planID) || v.outcome != "native_exact_remove_plan_not_consent" || s.store.Revision() != before || *pending || !removeRegistered(t, file) {
				t.Fatal("invalid/effectful capture", v, err)
			}
			plan := s.cleanupPlans[v.taskID]
			if plan.agentRemove == nil || plan.payload != nil || plan.diagnostic != nil || !plan.expires.Equal(plan.agentRemove.native.ExpiresAt()) || time.Until(plan.expires) > 30*time.Second {
				t.Fatal("second cache, fake cleanup, or extended native expiry")
			}
			for _, private := range []any{v, plan.agentRemove, plan.agentRemove.native} {
				raw, _ := json.Marshal(private)
				if string(raw) != "{}" {
					t.Fatal("private plan/view serialized", string(raw))
				}
			}
			v.agent, v.workspace = "other", "other" // returned value cannot replace retained target
			q = removeApply(q, v)
			applied, err := s.applyDiagnosticAgentRemove(context.Background(), q, "worker")
			if err != nil || applied.agent != "worker" || applied.workspace != target || !applied.dispatched || applied.problem != "none" || applied.outcome != "native_handler_returned_unverified" || removeRegistered(t, file) || !*pending || len(s.cleanupPlans) != 0 || provider.prepares != 1 {
				t.Fatal("native apply failed/overclaimed/replanned", applied, err)
			}
			if err := agentservice.CheckWorkspaceRemoval(plan.agentRemove.scope.workspace.Path); err != nil {
				t.Fatal("removed registration retained its workspace ownership association", err)
			}
			if _, err := s.applyDiagnosticAgentRemove(context.Background(), q, "worker"); err == nil {
				t.Fatal("lost reply/success replayed")
			}
		})
	}
}

func TestDiagnosticAgentRemovePrivateConsumesRefusedApply(t *testing.T) {
	for _, kind := range []string{"scope", "feedback", "parent", "agent", "agent-selector", "operation", "mode", "schema", "expired", "revision", "pending", "cancelled", "hidden", "registration", "ref", "schedule", "native-registration", "active-refresh"} {
		t.Run(kind, func(t *testing.T) {
			s, groups, _, q, file, pending := removeAdapterFixture(t, "origin")
			v, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
			if err != nil {
				t.Fatal(err)
			}
			q = removeApply(q, v)
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
			case "agent-selector":
				q.AgentID = "other"
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
			case "schedule":
				if err := os.WriteFile(filepath.Join(filepath.Dir(file), "schedules.json"), []byte("PRIVATE invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "native-registration":
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, append(raw, ' '), 0600); err != nil {
					t.Fatal(err)
				}
			case "active-refresh":
				s.refreshPlan = &diagnosticRefreshPlan{view: protocol.DiagnosticMaintenance{Status: "running"}}
			}
			if _, err := s.applyDiagnosticAgentRemove(ctx, q, id); err == nil || strings.Contains(err.Error(), "PRIVATE") || !removeRegistered(t, file) || len(s.cleanupPlans) != 0 {
				t.Fatal("refused apply changed state or retained plan", err)
			}
		})
	}
}

func TestDiagnosticAgentRemovePrivateFinalGuardRechecksScopeAfterNativeAdmission(t *testing.T) {
	for _, kind := range []string{"pending", "revision", "registration", "hidden", "ref", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			s, groups, provider, q, file, pending := removeAdapterFixture(t, "origin")
			v, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
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
				case "ref":
					groups.transform = func(tasks []protocol.Task) []protocol.Task {
						tasks[0].SourceRefs[0].Metadata = map[string]string{}
						return tasks
					}
				case "cancelled":
					cancel()
				}
			}
			if _, err := s.applyDiagnosticAgentRemove(ctx, removeApply(q, v), "worker"); err == nil || calls != 1 || !removeRegistered(t, file) || len(s.cleanupPlans) != 0 {
				t.Fatal("scope drift reached first native effect", calls, err)
			}
		})
	}
}

func TestDiagnosticAgentRemovePrivateRefusesWrongExactRefAndFeedbackScope(t *testing.T) {
	for _, kind := range []string{"other-agent-in-scoped-task", "duplicate", "id", "entity", "metadata-id", "workspace-id", "workspace-path", "mode", "hidden", "oversized", "unregistered-feedback", "origin-relation", "duplicate-provider"} {
		t.Run(kind, func(t *testing.T) {
			s, groups, provider, q, file, pending := removeAdapterFixture(t, "origin")
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
			if _, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker"); err == nil || provider.prepares != 0 || !removeRegistered(t, file) || *pending || len(s.cleanupPlans) != 0 {
				t.Fatal("unsafe scope reached native capture", err, provider.prepares)
			}
		})
	}
}

func TestDiagnosticAgentRemovePrivateRetentionBoundsPreservePreviousPreview(t *testing.T) {
	for _, kind := range []string{"zero-bytes", "oversized-native", "expired-native", "extended-native", "oversized-context", "preview-id", "negative-schedules", "negative-enabled", "enabled-over-total"} {
		t.Run(kind, func(t *testing.T) {
			s, groups, provider, q, _, _ := removeAdapterFixture(t, "origin")
			v, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "oversized-context" {
				groups.groups[0].NotePath = strings.Repeat("x", 300*1024)
			} else {
				provider.wrap = func(p integration.AgentRemovePlan) integration.AgentRemovePlan {
					wrapped := removeAdapterWrappedPlan{AgentRemovePlan: p, expires: p.ExpiresAt(), bytes: p.RetainedBytes()}
					switch kind {
					case "preview-id", "negative-schedules", "negative-enabled", "enabled-over-total":
						preview := p.Preview()
						switch kind {
						case "preview-id":
							preview.ID = "other"
						case "negative-schedules":
							preview.Schedules = -1
						case "negative-enabled":
							preview.EnabledSchedules = -1
						case "enabled-over-total":
							preview.EnabledSchedules = preview.Schedules + 1
						}
						wrapped.preview = &preview
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
			if _, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker"); err == nil || len(s.cleanupPlans) != 1 || s.cleanupPlans[v.taskID].id != v.planID {
				t.Fatal("invalid retention replaced previous plan", err)
			}
		})
	}
}

func TestDiagnosticAgentRemovePrivateSharedCacheSupersessionAndCapacity(t *testing.T) {
	s, _, _, q, _, _ := removeAdapterFixture(t, "origin")
	v, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := protocol.CleanupPreview{TaskID: v.taskID, Targets: []protocol.CleanupTarget{{Source: "git", Kind: "worktree", ResourceID: "other"}}}
	issued, err := s.issueCleanupPlan(context.Background(), cleanup, v.revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.applyDiagnosticAgentRemove(context.Background(), removeApply(q, v), "worker"); err == nil {
		t.Fatal("cleanup did not supersede removal")
	}
	v, err = s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.takeCleanupPlan(&issued); err == nil {
		t.Fatal("removal did not supersede cleanup")
	}
	for id := v.taskID + 1; len(s.cleanupPlans) < maxCleanupPlans; id++ {
		s.cleanupPlans[id] = nativeCleanupPlan{id: strings.Repeat("a", 64), revision: v.revision, expires: time.Now().Add(time.Minute)}
	}
	if _, err := s.issueCleanupPlan(context.Background(), protocol.CleanupPreview{TaskID: 999999, Targets: cleanup.Targets}, v.revision); err == nil || len(s.cleanupPlans) != maxCleanupPlans {
		t.Fatal("shared capacity bypassed")
	}
	latest, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
	if err != nil || latest.planID == v.planID || len(s.cleanupPlans) != maxCleanupPlans {
		t.Fatal("at-capacity same-task supersession failed", err)
	}
}

func TestDiagnosticAgentRemovePrivateNativeErrorAndCancellationAreNotResourceOutcomes(t *testing.T) {
	for _, kind := range []string{"error", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			s, _, provider, q, file, pending := removeAdapterFixture(t, "origin")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider.wrap = func(p integration.AgentRemovePlan) integration.AgentRemovePlan {
				w := removeAdapterWrappedPlan{AgentRemovePlan: p, expires: p.ExpiresAt(), bytes: p.RetainedBytes(), fail: kind == "error"}
				if kind == "cancelled" {
					w.after = cancel
				}
				return w
			}
			v, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.applyDiagnosticAgentRemove(ctx, removeApply(q, v), "worker")
			if err != nil || result.problem == "none" || result.outcome != "unverified_may_be_partial" || !result.dispatched || !*pending || removeRegistered(t, file) || len(s.cleanupPlans) != 0 {
				t.Fatal("native partial/cancelled return became success/rollback", result, err)
			}
			_, _, operations, _ := s.store.OperationInspectionSnapshot()
			if len(operations) != 0 {
				t.Fatal("private-only removal introduced unversioned public managed evidence", operations)
			}
		})
	}
}

func TestDiagnosticAgentRemovePrivateNativeCannotExecuteBeforeConsumedApply(t *testing.T) {
	s, _, _, q, file, pending := removeAdapterFixture(t, "origin")
	v, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupPlans[v.taskID].agentRemove.native.Execute(context.Background()); err == nil || !removeRegistered(t, file) || *pending {
		t.Fatal("detached/pre-apply native capture bypassed adapter")
	}
	if _, err := s.applyDiagnosticAgentRemove(context.Background(), removeApply(q, v), "worker"); err == nil || len(s.cleanupPlans) != 0 {
		t.Fatal("preconsumed native capture replayed")
	}
}

func TestDiagnosticAgentRemovePrivateHasNoRPCActivation(t *testing.T) {
	for _, mode := range []string{"preview", "apply"} {
		t.Run(mode, func(t *testing.T) {
			s, _, provider, q, file, pending := removeAdapterFixture(t, "origin")
			q.Mode = mode
			if mode == "apply" {
				q.PlanID = strings.Repeat("a", 64)
			}
			response := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &q})
			if response.OK || provider.prepares != 0 || !removeRegistered(t, file) || *pending || len(s.cleanupPlans) != 0 {
				t.Fatal("private removal unexpectedly enabled RPC", response)
			}
		})
	}
}

func TestDiagnosticAgentRemoveSharesStopRetentionWithoutCrossOperationExecution(t *testing.T) {
	for _, kind := range []string{"remove_supersedes_stop", "stop_supersedes_remove", "remove_cannot_take_stop", "stop_cannot_take_remove"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, q, file, pending := removeAdapterFixture(t, "origin")
			stopQ := q
			stopQ.Operation = "agent_stop"
			if kind == "remove_supersedes_stop" || kind == "remove_cannot_take_stop" {
				old, err := s.prepareDiagnosticAgentStop(context.Background(), stopQ, "worker")
				if err != nil {
					t.Fatal(err)
				}
				if kind == "remove_supersedes_stop" {
					v, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.applyDiagnosticAgentStop(context.Background(), stopApply(stopQ, old), "worker"); err == nil || s.cleanupPlans[v.taskID].id != v.planID {
						t.Fatal("stop survived removal supersession or consumed another plan")
					}
				} else {
					apply := q
					apply.Mode, apply.PlanID = "apply", old.planID
					if _, err := s.applyDiagnosticAgentRemove(context.Background(), apply, "worker"); err == nil || len(s.cleanupPlans) != 0 {
						t.Fatal("removal executed/restored a stop plan")
					}
				}
			} else {
				old, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
				if err != nil {
					t.Fatal(err)
				}
				if kind == "stop_supersedes_remove" {
					v, err := s.prepareDiagnosticAgentStop(context.Background(), stopQ, "worker")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.applyDiagnosticAgentRemove(context.Background(), removeApply(q, old), "worker"); err == nil || s.cleanupPlans[v.taskID].id != v.planID {
						t.Fatal("removal survived stop supersession or consumed another plan")
					}
				} else {
					apply := stopQ
					apply.Mode, apply.PlanID = "apply", old.planID
					if _, err := s.applyDiagnosticAgentStop(context.Background(), apply, "worker"); err == nil || len(s.cleanupPlans) != 0 {
						t.Fatal("stop executed/restored a removal plan")
					}
				}
			}
			if !removeRegistered(t, file) || *pending {
				t.Fatal("cross-operation refusal reached native effects")
			}
		})
	}
}

func TestDiagnosticAgentRemovePausedScheduleCountsRemainCaptureFacts(t *testing.T) {
	s, _, _, q, file, _ := removeAdapterFixture(t, "origin")
	path := filepath.Join(filepath.Dir(file), "schedules.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.applyDiagnosticAgentRemove(context.Background(), removeApply(q, v), "worker")
	if err != nil || result.schedules != 2 || result.enabledSchedules != 1 || result.record == "" {
		t.Fatal("apply rewrote capture facts or lost exact record", result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if json.Unmarshal(before, &want) != nil || json.Unmarshal(after, &got) != nil {
		t.Fatal("schedule state lost")
	}
	for _, v := range want["schedules"].([]any) {
		v.(map[string]any)["enabled"] = false
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("removal reset schedule identity, payloads or already-paused state")
	}
}

func TestDiagnosticAgentRemoveConcurrentApplyEntersNativeGuardOnce(t *testing.T) {
	s, _, provider, q, file, pending := removeAdapterFixture(t, "origin")
	entered := make(chan struct{}, 8)
	provider.guardHook = func() { entered <- struct{}{} }
	v, err := s.prepareDiagnosticAgentRemove(context.Background(), q, "worker")
	if err != nil {
		t.Fatal(err)
	}
	apply := removeApply(q, v)
	results := make(chan error, 8)
	for range 8 {
		go func() { _, err := s.applyDiagnosticAgentRemove(context.Background(), apply, "worker"); results <- err }()
	}
	success := 0
	for range 8 {
		if err := <-results; err == nil {
			success++
		}
	}
	if success != 1 || len(entered) != 1 || removeRegistered(t, file) || !*pending || len(s.cleanupPlans) != 0 {
		t.Fatal("concurrent admission replayed or lost native effects", success, len(entered))
	}
}
