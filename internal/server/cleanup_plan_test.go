package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

type cleanupPlanProvider struct {
	targets                             []protocol.CleanupTarget
	previews, admissions, checks, calls atomic.Int32
	fail                                bool
	failAt                              int32
	onPreview                           func()
}

func (*cleanupPlanProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "fixture"}
}
func (*cleanupPlanProvider) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	panic("cleanup must not collect")
}
func (p *cleanupPlanProvider) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	p.previews.Add(1)
	if p.onPreview != nil {
		p.onPreview()
	}
	return cloneCleanupPreview(protocol.CleanupPreview{Targets: p.targets}).Targets, nil
}
func (p *cleanupPlanProvider) AdmitCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	p.admissions.Add(1)
	return p, func() {}, nil
}
func (p *cleanupPlanProvider) PreflightCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error {
	p.checks.Add(1)
	return nil
}
func (p *cleanupPlanProvider) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	call := p.calls.Add(1)
	if p.fail || p.failAt == call {
		return req.Target, errors.New("private fixture failure")
	}
	if req.Target.Path != "" {
		if err := os.Remove(req.Target.Path); err != nil {
			return req.Target, err
		}
	}
	return req.Target, nil
}

func cloneCleanupPreview(preview protocol.CleanupPreview) protocol.CleanupPreview {
	raw, err := json.Marshal(preview)
	if err != nil {
		panic(err)
	}
	var cloned protocol.CleanupPreview
	if err := json.Unmarshal(raw, &cloned); err != nil {
		panic(err)
	}
	return cloned
}

// Existing execution/observation fixtures must also cross the real preview
// boundary. Their providers retain the original exact target payloads.
func previewFixtureCleanup(t *testing.T, s *Server) protocol.CleanupPreview {
	t.Helper()
	s.store.SetTasks([]protocol.Task{{Title: "private-task", SourceRefs: []protocol.SourceRef{{ID: "fixture:task", Source: "fixture", Kind: "thing", Role: protocol.SourceRefRoleAuthoritative}}}})
	tasks := s.store.Tasks()
	if len(tasks) != 1 {
		t.Fatal("cleanup fixture task unavailable", tasks)
	}
	preview, err := s.cleanupPreview(context.Background(), tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func cleanupPlanFixture(t *testing.T) (*Server, *cleanupPlanProvider, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("RADAR_STATE", filepath.Join(root, "state.db"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Title: "ABC-123", SourceRefs: []protocol.SourceRef{{ID: "fixture:one", Source: "fixture", Kind: "thing", Role: protocol.SourceRefRoleAuthoritative}}}})
	resource := filepath.Join(root, "resource")
	if err := os.WriteFile(resource, []byte("intact"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &cleanupPlanProvider{targets: []protocol.CleanupTarget{{Source: "fixture", SourceRefID: "fixture:one", Kind: "thing", ResourceID: "one", Path: resource, Operation: map[string]string{"private": "original"}, Safety: []protocol.CleanupSafety{{Kind: "local_changes", Message: "private warning", BlocksAutomatic: true}}}}}
	return New(store, logger, nil, nil, nil, integration.NewRegistry(provider), cleanup.New([]integration.CleanupProvider{provider})), provider, resource
}

func requireCleanupPlan(t *testing.T, s *Server) protocol.CleanupPreview {
	t.Helper()
	preview, err := s.cleanupPreview(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func requirePlanRejected(t *testing.T, s *Server, p *cleanupPlanProvider, resource string, preview protocol.CleanupPreview) {
	t.Helper()
	before := s.store.Revision()
	result, err := s.cleanup(context.Background(), &preview)
	if err == nil || len(result.Targets) != 0 || result.OperationID != "" || p.admissions.Load() != 0 || p.checks.Load() != 0 || p.calls.Load() != 0 || s.store.Revision() != before {
		t.Fatalf("stale/changed/unissued preview reached native execution: result=%+v err=%v admission=%d checks=%d calls=%d", result, err, p.admissions.Load(), p.checks.Load(), p.calls.Load())
	}
	if got, err := os.ReadFile(resource); err != nil || string(got) != "intact" {
		t.Fatal("rejected preview changed fixture resource", err)
	}
}

func TestCleanupPlanRejectsUnissuedOrChangedBundle(t *testing.T) {
	mutations := map[string]func(*protocol.CleanupPreview){
		"unissued": func(p *protocol.CleanupPreview) { p.PlanID = "" },
		"invented": func(p *protocol.CleanupPreview) {
			p.PlanID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		"task":              func(p *protocol.CleanupPreview) { p.TaskID++ },
		"title":             func(p *protocol.CleanupPreview) { p.TaskTitle = "changed" },
		"source":            func(p *protocol.CleanupPreview) { p.Targets[0].Source = "other" },
		"reference":         func(p *protocol.CleanupPreview) { p.Targets[0].SourceRefID = "other" },
		"resource":          func(p *protocol.CleanupPreview) { p.Targets[0].ResourceID = "other" },
		"path":              func(p *protocol.CleanupPreview) { p.Targets[0].Path += "-other" },
		"kind":              func(p *protocol.CleanupPreview) { p.Targets[0].Kind = "other" },
		"workspace":         func(p *protocol.CleanupPreview) { p.Targets[0].WorkspaceID = "other" },
		"branch":            func(p *protocol.CleanupPreview) { p.Targets[0].Branch = "other" },
		"private_operation": func(p *protocol.CleanupPreview) { p.Targets[0].Operation["private"] = "changed" },
		"private_safety":    func(p *protocol.CleanupPreview) { p.Targets[0].Safety[0].Message = "changed" },
		"effects":           func(p *protocol.CleanupPreview) { p.Targets[0].ProvidesWorkspace = true },
		"presentation":      func(p *protocol.CleanupPreview) { p.Targets[0].Presentation.Detail = "changed" },
		"omitted_target":    func(p *protocol.CleanupPreview) { p.Targets = nil },
		"extra_target":      func(p *protocol.CleanupPreview) { p.Targets = append(p.Targets, p.Targets[0]) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s, provider, resource := cleanupPlanFixture(t)
			preview := requireCleanupPlan(t, s)
			mutate(&preview)
			requirePlanRejected(t, s, provider, resource, preview)
		})
	}
}

func TestCleanupPlanRejectsChangedRevisionAndSupersededPreview(t *testing.T) {
	for _, supersede := range []bool{false, true} {
		t.Run(map[bool]string{false: "revision", true: "new_preview"}[supersede], func(t *testing.T) {
			s, provider, resource := cleanupPlanFixture(t)
			preview := requireCleanupPlan(t, s)
			if supersede {
				requireCleanupPlan(t, s)
			} else {
				s.store.SetTasks([]protocol.Task{})
			}
			requirePlanRejected(t, s, provider, resource, preview)
		})
	}
}

func TestCleanupPlanNativeErrorCannotBeReplayed(t *testing.T) {
	s, provider, resource := cleanupPlanFixture(t)
	provider.fail = true
	preview := requireCleanupPlan(t, s)
	first, err := s.cleanup(context.Background(), &preview)
	if err == nil || first.OperationID == "" || provider.calls.Load() != 1 {
		t.Fatal("fixture must reach actual native failure", first, err)
	}
	before := s.store.Revision()
	again, err := s.cleanup(context.Background(), &preview)
	if err == nil || again.OperationID != "" || provider.calls.Load() != 1 || s.store.Revision() != before {
		t.Fatal("failed/unknown outcome was replayed", again, err)
	}
	if _, err := os.Stat(resource); err != nil {
		t.Fatal("fixture failure unexpectedly removed resource", err)
	}
}

func TestCleanupPlanPartialFailureCannotReplayOrRemoveLaterTarget(t *testing.T) {
	s, provider, firstPath := cleanupPlanFixture(t)
	secondPath, lastPath := firstPath+"-second", firstPath+"-last"
	for _, path := range []string{secondPath, lastPath} {
		if err := os.WriteFile(path, []byte("intact"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	provider.targets = append(provider.targets,
		protocol.CleanupTarget{Source: "fixture", Kind: "thing", ResourceID: "second", Path: secondPath},
		protocol.CleanupTarget{Source: "fixture", Kind: "thing", ResourceID: "last", Path: lastPath})
	provider.failAt = 2
	preview := requireCleanupPlan(t, s)
	result, err := s.cleanup(context.Background(), &preview)
	if err == nil || len(result.Targets) != 1 || result.Targets[0].Path != firstPath || provider.calls.Load() != 2 || result.ExecutionObservation == nil || result.ExecutionObservation.Stage != "provider_call" {
		t.Fatal("exact plan changed native ordered partial failure", result, err)
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatal("successful native prefix did not remove first fixture", err)
	}
	for _, path := range []string{secondPath, lastPath} {
		if contents, err := os.ReadFile(path); err != nil || string(contents) != "intact" {
			t.Fatal("failed/unattempted fixture removed", path, err)
		}
	}
	before := s.store.Revision()
	if again, err := s.cleanup(context.Background(), &preview); err == nil || again.OperationID != "" || provider.calls.Load() != 2 || before != s.store.Revision() {
		t.Fatal("partially completed bundle replayed", again, err)
	}
}

func TestCleanupPlanRejectsChangedTargetOrder(t *testing.T) {
	s, provider, resource := cleanupPlanFixture(t)
	provider.targets = append(provider.targets, protocol.CleanupTarget{Source: "fixture", Kind: "thing", ResourceID: "second"})
	preview := requireCleanupPlan(t, s)
	preview.Targets[0], preview.Targets[1] = preview.Targets[1], preview.Targets[0]
	requirePlanRejected(t, s, provider, resource, preview)
}

func TestCleanupPlanRejectsRevisionDriftDuringPreview(t *testing.T) {
	s, provider, _ := cleanupPlanFixture(t)
	provider.onPreview = func() { s.store.SetTasks([]protocol.Task{}) }
	if preview, err := s.cleanupPreview(context.Background(), 1); err == nil {
		t.Fatal("stale provider snapshot received an executable identity", preview)
	}
	if provider.calls.Load() != 0 {
		t.Fatal("preview executed cleanup")
	}
}

func TestCleanupPlanExpiryAndDaemonRestartInvalidatePreview(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "restart"}[restart], func(t *testing.T) {
			s, provider, resource := cleanupPlanFixture(t)
			preview := requireCleanupPlan(t, s)
			if !hexPlanID(preview.PlanID) {
				t.Fatal("daemon did not issue a random opaque plan ID")
			}
			if restart {
				s = New(s.store, s.logger, nil, nil, nil, s.integrations, s.cleanupService)
			} else {
				plan := s.cleanupPlans[preview.TaskID]
				plan.expires = time.Now().Add(-time.Second)
				s.cleanupPlans[preview.TaskID] = plan
			}
			requirePlanRejected(t, s, provider, resource, preview)
		})
	}
}

func TestCleanupPlanPendingObservationInvalidatesRatherThanRevivesPlan(t *testing.T) {
	s, provider, resource := cleanupPlanFixture(t)
	var pending atomic.Bool
	s.SetResourcePublication(nil, func() protocol.Response { return protocol.Response{ResourceRefreshPending: pending.Load()} })
	preview := requireCleanupPlan(t, s)
	pending.Store(true)
	if result, err := s.cleanup(context.Background(), &preview); err == nil || !result.ResourceRefreshPending || result.OperationID != "" {
		t.Fatal("pending plan reached execution", result, err)
	}
	pending.Store(false) // even without a revision bump, the rejected plan is dead
	requirePlanRejected(t, s, provider, resource, preview)
}

func TestCleanupPlanConcurrentApplyEntersNativeExecutorAtMostOnce(t *testing.T) {
	s, provider, resource := cleanupPlanFixture(t)
	preview := requireCleanupPlan(t, s)
	const callers = 8
	var wg sync.WaitGroup
	var accepted atomic.Int32
	start := make(chan struct{})
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := s.cleanup(context.Background(), &preview)
			if err == nil && len(result.Targets) == 1 {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if accepted.Load() != 1 || provider.calls.Load() != 1 || provider.admissions.Load() != 1 || provider.checks.Load() != 1 {
		t.Fatal("one-use admission was replayed or valid bundle rejected", accepted.Load(), provider.calls.Load())
	}
	if _, err := os.Stat(resource); !os.IsNotExist(err) {
		t.Fatal("accepted fixture cleanup did not remove its resource", err)
	}
	if len(s.store.DashboardSnapshot().Operations) != 1 {
		t.Fatal("rejected duplicate created another operation")
	}
}

func TestCleanupPlanDoesNotReplaceFreshNativeOwnerGuards(t *testing.T) {
	s, provider, resource := cleanupPlanFixture(t)
	preview := requireCleanupPlan(t, s)
	owner := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "abc-owner")
	if err := os.MkdirAll(owner, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(agentservice.Registration{Version: 1, ID: "abc-owner", Desired: false, Entrypoint: "fixture", Workspace: protocol.AgentWorkspaceBinding{ID: "ABC-workspace", Path: resource, Mode: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "registration.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	owners, err := agentservice.WorkspaceRemovalOwners([]string{resource})
	if err != nil || len(owners[resource]) != 1 {
		t.Fatal("stopped-owner fixture did not establish native removal hold", owners, err)
	}
	result, err := s.cleanup(context.Background(), &preview)
	if err == nil || result.ExecutionObservation == nil || result.ExecutionObservation.Stage != "ownership_and_leases" || provider.calls.Load() != 0 || provider.admissions.Load() != 0 {
		t.Fatal("issued plan bypassed newly registered stopped owner", result, err)
	}
	if err := os.RemoveAll(owner); err != nil {
		t.Fatal(err)
	}
	// Removing the blocker is not authority to replay a consumed plan.
	requirePlanRejected(t, s, provider, resource, preview)
}

func TestCleanupPlanCancellationConsumesExactPlan(t *testing.T) {
	s, provider, resource := cleanupPlanFixture(t)
	preview := requireCleanupPlan(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := s.cleanup(ctx, &preview)
	if err == nil || result.OperationID == "" || provider.calls.Load() != 0 {
		t.Fatal("cancelled exact plan did not stop native execution", result, err)
	}
	requirePlanRejected(t, s, provider, resource, preview)
}

func TestCleanupPlanPrivateSnapshotAndBoundedRetention(t *testing.T) {
	s, provider, resource := cleanupPlanFixture(t)
	preview := requireCleanupPlan(t, s)
	provider.targets[0].Operation["private"] = "provider changed after return"
	// The stored bytes must not alias either the provider or returned preview.
	if strings.Contains(string(s.cleanupPlans[1].payload), "changed after return") {
		t.Fatal("retained plan aliases provider-owned data")
	}
	preview.Targets[0].Operation["private"] = "client changed after return"
	requirePlanRejected(t, s, provider, resource, preview)
	base := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "thing"}}}
	for taskID := 1; taskID <= maxCleanupPlans; taskID++ {
		base.TaskID = taskID
		if _, err := s.issueCleanupPlan(context.Background(), base, s.store.Revision()); err != nil {
			t.Fatal("bounded fixture admission", err)
		}
	}
	base.TaskID++
	if _, err := s.issueCleanupPlan(context.Background(), base, s.store.Revision()); err == nil || len(s.cleanupPlans) != maxCleanupPlans {
		t.Fatal("unbounded private plan retention")
	}
	for taskID, plan := range s.cleanupPlans {
		plan.expires = time.Now().Add(-time.Second)
		s.cleanupPlans[taskID] = plan
	}
	if _, err := s.issueCleanupPlan(context.Background(), base, s.store.Revision()); err != nil || len(s.cleanupPlans) != 1 {
		t.Fatal("expired private snapshots not reclaimed", err)
	}
}

func TestCleanupPlanOversizedNativeBundleAndCancelledPreviewAreUnavailable(t *testing.T) {
	for _, mode := range []string{"targets", "payload", "cancelled", "pending_during_preview"} {
		t.Run(mode, func(t *testing.T) {
			s, provider, _ := cleanupPlanFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "targets":
				provider.targets = make([]protocol.CleanupTarget, 513)
			case "payload":
				provider.targets[0].Description = strings.Repeat("x", maxCleanupPlanBytes)
			case "cancelled":
				provider.onPreview = cancel
			case "pending_during_preview":
				var pending atomic.Bool
				s.SetResourcePublication(nil, func() protocol.Response { return protocol.Response{ResourceRefreshPending: pending.Load()} })
				provider.onPreview = func() { pending.Store(true) }
			}
			if preview, err := s.cleanupPreview(ctx, 1); err == nil || preview.PlanID != "" || len(s.cleanupPlans) != 0 || provider.calls.Load() != 0 {
				t.Fatal("unavailable native evidence received a plan", preview, err)
			}
		})
	}
}

func TestCleanupPlanRPCRejectsUnknownFieldsAndMixedSelectors(t *testing.T) {
	for _, mode := range []string{"preview_extra", "apply_extra", "apply_nested", "apply_mixed"} {
		t.Run(mode, func(t *testing.T) {
			s, provider, resource := cleanupPlanFixture(t)
			preview := requireCleanupPlan(t, s)
			raw, _ := json.Marshal(preview)
			request := `{"method":"cleanup","cleanup":` + string(raw) + `}`
			switch mode {
			case "preview_extra":
				request = `{"method":"cleanup-preview","task_id":1,"command":"private-command"}`
			case "apply_extra":
				request = strings.TrimSuffix(request, "}") + `,"command":"private-command"}`
			case "apply_nested":
				request = strings.Replace(request, `"targets":`, `"unknown":"private-value","targets":`, 1)
			case "apply_mixed":
				request = strings.TrimSuffix(request, "}") + `,"task_id":1}`
			}
			daemon, client := net.Pipe()
			defer client.Close()
			go s.handle(daemon)
			if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			before := s.store.Revision()
			if _, err := io.WriteString(client, request+"\n"); err != nil {
				t.Fatal(err)
			}
			var result protocol.Response
			if err := json.NewDecoder(client).Decode(&result); err != nil || result.OK || result.CleanupResult != nil || provider.calls.Load() != 0 || s.store.Revision() != before {
				t.Fatal("inexact cleanup request accepted", result, err)
			}
			if _, err := os.Stat(resource); err != nil {
				t.Fatal("inexact request changed resource", err)
			}
		})
	}
}
