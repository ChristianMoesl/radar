package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	gitsource "radar/internal/integration/git"
	"radar/internal/integration/sbx"
	"radar/internal/integration/tmux"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/operationlock"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacelease"
)

func retirementFixture(t *testing.T) (*state.Store, string, workspacegroup.Workspace, protocol.Task) {
	t.Helper()
	store, root, path := fixture(t)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "private-name", Path: path, SessionName: "private-session", Sandbox: &workspacegroup.Sandbox{Name: "private-sandbox", Agent: "shell", EnvFile: filepath.Join(root, "private-env-file"), ReadyCommand: []string{"private-command"}}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	task := task(path, "test:ABC-123")
	task.SourceRefs[0].Metadata["completed_at"] = time.Now().Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339)
	task.SourceRefs = append(task.SourceRefs, protocol.SourceRef{ID: "private-runtime", Source: "sbx", Kind: "sandbox", Path: path, Metadata: map[string]string{"name": group.Sandbox.Name, "id": "private-lifetime"}}, protocol.SourceRef{ID: "private-session-lifetime", Source: "tmux", Kind: "session", Title: group.SessionName, Path: path})
	store.SetTasks([]protocol.Task{task})
	return store, root, group, task
}

func TestRetirementPreviewNativeParityPrivacyScopeAndNoSideEffects(t *testing.T) {
	store, root, group, task := retirementFixture(t)
	providers := integration.NewRegistry(workspace.Source{}, gitsource.Source{}, sbx.Source{}, tmux.Source{})
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture must include UI-hidden retained completed work")
	}
	records, refs, _ := store.InspectionSnapshot()
	native, err := cleanup.New(providers.CleanupProviders()).Preview(context.Background(), productionTask(records[0], refs), integration.CleanupRetire)
	if err != nil || len(native.Targets) != 3 {
		t.Fatalf("native=%+v %v", native, err)
	}
	// A second associated workspace with a missing anchor must not be inspected.
	task.SourceRefs = append(task.SourceRefs, protocol.SourceRef{ID: "workspace:unrelated", Source: "workspace", Kind: "workspace", WorkspaceID: "unrelated", Path: filepath.Join(root, "unregistered"), ProvidesWorkspace: true})
	store.SetTasks([]protocol.Task{task})
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	for _, name := range []string{"git", "sbx", "tmux"} {
		if err := os.WriteFile(filepath.Join(bin, name), fmt.Appendf(nil, "#!/bin/sh\n: > %q\nexit 1\n", marker), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Preview must neither acquire nor mistake either live lock for permission.
	releaseOperation, err := operationlock.Acquire(false)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOperation()
	releaseLease, err := workspacelease.Acquire(group.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLease()
	beforeRecords, beforeRefs, revision := store.InspectionSnapshot()
	beforeRegistry, _ := os.ReadFile(workspacegroup.Path(root))
	req := protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID, Workspace: group.Path, Limit: 1}
	page, err := Probe(context.Background(), store, req, true, providers)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || !page.Complete || page.NextCursor != "" || !page.ResourceRefreshPending || page.Schema != protocol.DiagnosticsVersion {
		t.Fatalf("page=%+v %v", page, err)
	}
	view := page.Items[0].RetirementPreview
	if view == nil || view.ID != group.ID || view.Stage != "runtime_retirement" || view.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" || view.OwnershipEvidence != "fresh_bounded_registration" || view.AgentOwnerCount == nil || *view.AgentOwnerCount != 0 || view.LeaseEvidence != "unknown_not_checked" || len(view.Targets) != len(native.Targets) || len(view.Preserved) != 4 {
		t.Fatalf("incorrect preview=%+v", view)
	}
	for i, target := range view.Targets {
		if target.Provider != native.Targets[i].Source || target.SafetyNoticeCount != len(native.Targets[i].Safety) || target.AutomaticBlockerCount != len(cleanup.AutomaticBlockingMessages([]protocol.CleanupTarget{native.Targets[i]}, false)) || !strings.HasPrefix(target.ID, "retirement-target:") {
			t.Fatalf("native parity lost: %+v %+v", target, native.Targets[i])
		}
	}
	encoded, _ := json.Marshal(page)
	for _, private := range []string{"private-", "operation\"", "task_title", "session_name", "sandbox_name", "runtime_id"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private/native operation escaped: %s", encoded)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("retirement preview ran a subprocess")
	}
	afterRecords, afterRefs, afterRevision := store.InspectionSnapshot()
	afterRegistry, _ := os.ReadFile(workspacegroup.Path(root))
	if revision != afterRevision || !reflect.DeepEqual(beforeRecords, afterRecords) || !reflect.DeepEqual(beforeRefs, afterRefs) || string(beforeRegistry) != string(afterRegistry) {
		t.Fatal("preview changed retained state or registration")
	}
	for _, invalid := range []protocol.DiagnosticsRequest{{Section: "retirement_preview"}, {Section: "retirement_preview", ParentID: "unknown"}, {Section: "retirement_preview", ParentID: group.ID, Workspace: group.Path + "-sibling"}, {Section: "retirement_preview", ParentID: group.ID, Workspace: root}, {Section: "retirement_preview", ParentID: group.ID, ID: "row"}, {Section: "retirement_preview", ParentID: group.ID, State: "done"}, {Section: "retirement_preview", ParentID: group.ID, Cursor: "cursor"}, {Section: "retirement_preview", ParentID: group.ID, Limit: 51}} {
		if _, err := Probe(context.Background(), store, invalid, false, providers); err == nil {
			t.Fatalf("invalid preview accepted: %+v", invalid)
		}
	}
	if _, err := Read(store, req, false, providers); err == nil {
		t.Fatal("cached inventory implicitly planned retirement")
	}
}

type retirementPlannerFixture struct {
	workspace.Source
	calls int
	run   func(context.Context, int, []protocol.CleanupTarget) ([]protocol.CleanupTarget, error)
}

func (p *retirementPlannerFixture) DiagnosticRetirementTargets(ctx context.Context, task protocol.Task, id string) ([]protocol.CleanupTarget, error) {
	p.calls++
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > RuntimeProbeTimeout {
		panic("preview lacks bounded context")
	}
	targets, err := p.Source.DiagnosticRetirementTargets(ctx, task, id)
	if err != nil {
		return nil, err
	}
	return p.run(ctx, p.calls, targets)
}
func (*retirementPlannerFixture) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	panic("full cleanup preview is not an observational capability")
}
func (*retirementPlannerFixture) Cleanup(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error) {
	panic("preview cannot execute cleanup")
}
func (*retirementPlannerFixture) PreflightCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error {
	panic("preview cannot run execution preflight")
}

func TestRetirementPreviewRejectsNativeFailureDriftCancellationAndInvalidTargets(t *testing.T) {
	for _, scenario := range []string{"provider error", "registration drift", "revision drift", "operation drift", "aliased drift", "deadline", "private provider", "foreign workspace", "foreign path", "wrong identity", "wrong kind", "wrong workspace flag", "missing workspace", "missing runtime", "duplicate provider", "too many targets"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, group, task := retirementFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := &retirementPlannerFixture{}
			var firstTargets []protocol.CleanupTarget
			provider.run = func(ctx context.Context, call int, targets []protocol.CleanupTarget) ([]protocol.CleanupTarget, error) {
				switch scenario {
				case "provider error":
					return nil, errors.New("private-provider-error")
				case "registration drift":
					if call == 2 {
						group.SessionName = "private-replaced"
						if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
							t.Fatal(err)
						}
					}
				case "revision drift":
					task.Title += "changed"
					store.SetTasks([]protocol.Task{task})
				case "operation drift":
					if call == 2 {
						targets[0].Operation["runtime_id"] = "private-replaced"
					}
				case "aliased drift":
					if call == 1 {
						firstTargets = targets
					} else {
						firstTargets[0].Operation["runtime_id"] = "private-replaced"
					}
					targets = firstTargets
				case "deadline":
					cancel()
				case "private provider":
					targets[0].Source = "private-provider"
				case "foreign workspace":
					targets[0].WorkspaceID = "foreign"
				case "foreign path":
					targets[0].Path += "-sibling"
				case "wrong identity":
					targets[0].ResourceID = "foreign"
				case "wrong kind":
					targets[0].Kind = "worktree"
				case "wrong workspace flag":
					targets[0].ProvidesWorkspace = true
				case "missing workspace":
					targets = targets[:2]
				case "missing runtime":
					targets = targets[1:]
				case "duplicate provider":
					targets[0] = targets[1]
				case "too many targets":
					targets = append(targets, targets[0])
				}
				return targets, nil
			}
			_, err := Probe(ctx, store, protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID}, false, integration.NewRegistry(provider))
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe/native result or raw error accepted: %v", err)
			}
		})
	}
}

func TestRetirementPreviewNativeRegistrationAndStageFailuresRemainUnavailable(t *testing.T) {
	for _, scenario := range []string{"retention", "expired", "activity", "review", "not done", "missing registration", "missing anchor", "symlink anchor", "shared session", "shared sandbox", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, group, task := retirementFixture(t)
			providers := integration.NewRegistry(workspace.Source{})
			switch scenario {
			case "retention":
				task.SourceRefs[0].Metadata["completed_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			case "expired":
				task.SourceRefs[0].Metadata["completed_at"] = time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
			case "activity":
				task.SourceRefs[0].Activity = protocol.ActivityBusy
			case "review":
				task.SourceRefs[1].AgentReview = true
			case "not done":
				task.SourceRefs[0].Signal = "in_progress"
			case "missing registration":
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version}); err != nil {
					t.Fatal(err)
				}
			case "missing anchor":
				if err := os.Remove(group.Path); err != nil {
					t.Fatal(err)
				}
			case "symlink anchor":
				if err := os.Remove(group.Path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), group.Path); err != nil {
					t.Fatal(err)
				}
			case "shared session", "shared sandbox":
				otherPath := filepath.Join(root, "other")
				other := workspacegroup.Workspace{ID: workspacegroup.ID(otherPath), Name: "other", Path: otherPath}
				if scenario == "shared session" {
					other.SessionName = group.SessionName
				} else {
					other.Sandbox = &workspacegroup.Sandbox{Name: group.Sandbox.Name, Agent: "shell"}
				}
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group, other}}); err != nil {
					t.Fatal(err)
				}
			case "unsupported":
				// Expose registration observation but deliberately not the optional
				// retirement-planner capability inherited by workspace.Source.
				providers = integration.NewRegistry(struct {
					integration.WorkspaceRegistrationDiagnostics
				}{workspace.Source{}})
			}
			store.SetTasks([]protocol.Task{task})
			if _, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID}, false, providers); err == nil {
				t.Fatal("unsupported, stale or non-retirement candidate was previewed")
			}
		})
	}
}

type delayedPreviewDeadline struct {
	context.Context
	deadline time.Time
}

func (c delayedPreviewDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func TestRetirementPreviewHonorsAbsoluteDeadlineWithoutContextTimer(t *testing.T) {
	store, _, group, _ := retirementFixture(t)
	for _, expiredAtEntry := range []bool{true, false} {
		provider := &retirementPlannerFixture{}
		provider.run = func(_ context.Context, _ int, targets []protocol.CleanupTarget) ([]protocol.CleanupTarget, error) {
			time.Sleep(20 * time.Millisecond)
			return targets, nil
		}
		delay := 10 * time.Millisecond
		if expiredAtEntry {
			delay = -time.Second
		}
		ctx := delayedPreviewDeadline{Context: context.Background(), deadline: time.Now().Add(delay)}
		if _, err := Probe(ctx, store, protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID}, false, integration.NewRegistry(provider)); err == nil {
			t.Fatal("expired evidence was accepted before the inherited context timer fired")
		}
		if expiredAtEntry && provider.calls != 0 {
			t.Fatal("expired request called native planner")
		}
	}
}

func TestRetirementPreviewBoundsNativeNoticesAndSupportsNoRegisteredRuntime(t *testing.T) {
	store, root, group, _ := retirementFixture(t)
	provider := &retirementPlannerFixture{}
	provider.run = func(_ context.Context, _ int, targets []protocol.CleanupTarget) ([]protocol.CleanupTarget, error) {
		targets[0].Safety = []protocol.CleanupSafety{{Kind: "private-kind", Summary: "private-summary", Message: "private-error-token", BlocksAutomatic: true}}
		return targets, nil
	}
	request := protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID}
	page, err := Probe(context.Background(), store, request, false, integration.NewRegistry(provider))
	if err != nil {
		t.Fatal(err)
	}
	if target := page.Items[0].RetirementPreview.Targets[0]; target.SafetyNoticeCount != 1 || target.AutomaticBlockerCount != 1 {
		t.Fatalf("notice counts=%+v", target)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "private-") {
		t.Fatal("raw native safety text escaped")
	}
	firstIDs := []string{}
	for _, target := range page.Items[0].RetirementPreview.Targets {
		firstIDs = append(firstIDs, target.ID)
	}
	page, err = Probe(context.Background(), store, request, false, integration.NewRegistry(provider))
	if err != nil {
		t.Fatal(err)
	}
	for i, target := range page.Items[0].RetirementPreview.Targets {
		if target.ID != firstIDs[i] {
			t.Fatal("stable target identity changed on repeated preview")
		}
	}
	group.Sandbox, group.SessionName = nil, ""
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	page, err = Probe(context.Background(), store, request, false, integration.NewRegistry(workspace.Source{}))
	if err != nil || len(page.Items[0].RetirementPreview.Targets) != 1 || page.Items[0].RetirementPreview.Targets[0].Provider != "workspace" {
		t.Fatalf("no-runtime proposal=%+v %v", page, err)
	}
}
