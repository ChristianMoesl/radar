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
	"radar/internal/integration/tmux"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/operationlock"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestCleanupPreviewRealNativeBundleAcrossWorkspacesAndHeldLocks(t *testing.T) {
	store, root, path := fixture(t)
	other := filepath.Join(root, "other")
	groups := []workspacegroup.Workspace{}
	task := task(path, "test:ABC-123")
	for i, anchor := range []string{path, other} {
		if err := os.MkdirAll(anchor, 0700); err != nil {
			t.Fatal(err)
		}
		id := workspacegroup.ID(anchor)
		groups = append(groups, workspacegroup.Workspace{ID: id, Name: fmt.Sprintf("private-%d", i), Path: anchor})
		if i > 0 {
			task.SourceRefs = append(task.SourceRefs, protocol.SourceRef{ID: "workspace:" + id, Source: "workspace", Kind: "workspace", Path: anchor, WorkspaceID: id, ProvidesWorkspace: true})
		}
		task.SourceRefs = append(task.SourceRefs, protocol.SourceRef{ID: fmt.Sprintf("private-session-%d", i), Source: "tmux", Kind: "session", Title: fmt.Sprintf("private-session-%d", i), Path: anchor})
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: groups}); err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{task})
	records, refs, revision := store.InspectionSnapshot()
	providers := integration.NewRegistry(workspace.Source{}, tmux.Source{})
	native, err := cleanup.New(providers.CleanupProviders()).Preview(context.Background(), productionTask(records[0], refs), integration.CleanupConfirmed)
	if err != nil || len(native.Targets) != 4 {
		t.Fatalf("native targets=%+v %v", native, err)
	}
	beforeRegistry, _ := os.ReadFile(workspacegroup.Path(root))
	retirementOwner(t, other) // A stopped owner in the other workspace still blocks.
	release, err := operationlock.Acquire(false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	lease, err := workspacelease.Acquire(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease()
	req := protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: fmt.Sprint(records[0].NumericID), Workspace: path, Limit: 1}
	count := 0
	blocked := 0
	leaseBlocked := 0
	for {
		page, err := Probe(context.Background(), store, req, false, providers)
		if err != nil || page.Total != len(native.Targets) {
			t.Fatalf("page=%+v %v", page, err)
		}
		count += len(page.Items)
		for _, row := range page.Items {
			view := row.CleanupPreview
			if view.OwnershipEvidence != "fresh_bounded_registration" || view.AgentOwnerCount == nil {
				t.Fatal("real native bundle lost registered ownership evidence")
			}
			if view.ExecutionEligibility == "blocked_registered_agent" {
				blocked++
			}
			if view.ExecutionEligibility == "blocked_lease_contention" && view.LeaseEvidence == "contended_at_probe" {
				leaseBlocked++
			}
		}
		if page.Complete {
			break
		}
		req.Cursor = page.NextCursor
	}
	afterRegistry, _ := os.ReadFile(workspacegroup.Path(root))
	if count != 4 || blocked != 2 || leaseBlocked != 2 || store.Revision() != revision || string(beforeRegistry) != string(afterRegistry) {
		t.Fatal("incomplete bundle or changed registration/cache")
	}
	for _, anchor := range []string{path, other} {
		if _, err := os.Stat(anchor); err != nil {
			t.Fatal("preview removed workspace", err)
		}
	}
}

type cleanupDiagnosticProvider struct {
	name    string
	targets []protocol.CleanupTarget
	calls   int
	run     func(context.Context)
	err     error
	task    protocol.Task
	mode    integration.CleanupMode
}

func (p *cleanupDiagnosticProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: p.name}
}
func (*cleanupDiagnosticProvider) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	panic("collection")
}
func (*cleanupDiagnosticProvider) Cleanup(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error) {
	panic("execution")
}
func (p *cleanupDiagnosticProvider) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	p.calls++
	p.task = req.Task
	expected := p.mode
	if expected == integration.CleanupSafe {
		expected = integration.CleanupConfirmed
	}
	if req.Mode != expected {
		panic("wrong native cleanup mode")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > CleanupPreviewTimeout {
		panic("unbounded native preview")
	}
	if p.run != nil {
		p.run(ctx)
	}
	return p.targets, p.err
}

type cleanupDiagnosticRegistration struct {
	cleanupDiagnosticProvider
	groups  []protocol.DiagnosticWorkspace
	observe func()
}

func (p *cleanupDiagnosticRegistration) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	if p.observe != nil {
		p.observe()
	}
	return p.groups, nil
}

func manualPreviewFixture(t *testing.T, count int) (*cleanupDiagnosticProvider, *cleanupDiagnosticRegistration) {
	t.Helper()
	provider := &cleanupDiagnosticProvider{name: "git"}
	for i := 0; i < count; i++ {
		provider.targets = append(provider.targets, protocol.CleanupTarget{Source: "git", Kind: "worktree", ResourceID: fmt.Sprintf("private-worktree-%d", i), Path: fmt.Sprintf("/private-path/%d", i), Title: "private-title", Description: "private-description", Operation: map[string]string{"delete_branch": "private-branch", "token": "private-token"}, Safety: []protocol.CleanupSafety{{Kind: "local_changes", Message: "private-file", BlocksAutomatic: true, Expires: true}, {Kind: "private-kind", Message: "private-error", BlocksAutomatic: true}}})
	}
	return provider, &cleanupDiagnosticRegistration{cleanupDiagnosticProvider: cleanupDiagnosticProvider{name: "workspace"}}
}

func TestCleanupPreviewNativeParityPrivacyCompletePaginationScopeAndNoExecution(t *testing.T) {
	store, _, path := fixture(t)
	store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
	records, refs, revision := store.InspectionSnapshot()
	provider, registration := manualPreviewFixture(t, 25)
	providers := integration.NewRegistry(provider, registration)
	beforeDisk, _ := os.ReadFile(os.Getenv("RADAR_STATE"))
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture must account for UI-hidden completed work")
	}
	req := protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID, Workspace: path, Limit: 7}
	seen := map[string]bool{}
	firstCursor := ""
	firstID := ""
	for {
		page, err := Probe(context.Background(), store, req, false, providers)
		if err != nil || page.Schema != protocol.DiagnosticsVersion || page.Total != 25 || len(page.Items) > 7 || page.ResourceRefreshPending {
			t.Fatalf("page=%+v %v", page, err)
		}
		if firstCursor == "" {
			firstCursor = page.NextCursor
			firstID = page.Items[0].ID
		}
		for _, row := range page.Items {
			v := row.CleanupPreview
			if v == nil || seen[row.ID] || v.ID != row.ID || v.TaskID != records[0].NumericID || v.RecordID != records[0].ID || v.Provider != "git" || v.Effect != "remove_worktree_and_local_changes" || v.BranchEffect != "delete_local_branch" || v.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" || v.SafetyNoticeCount != 2 || v.AutomaticBlockerCount != 2 || len(v.Safety) != 2 {
				t.Fatalf("native target projection=%+v", v)
			}
			seen[row.ID] = true
		}
		raw, _ := json.Marshal(page)
		if len(raw) > 32*1024 || strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "operation\"") {
			t.Fatalf("private/oversized output: %s", raw)
		}
		if page.Complete {
			if page.NextCursor != "" {
				t.Fatal("terminal cursor")
			}
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 25 || provider.calls != 4 || !reflect.DeepEqual(provider.task, productionTask(records[0], refs)) {
		t.Fatal("native planner did not receive complete cached production task")
	}
	afterRecords, afterRefs, afterRevision := store.InspectionSnapshot()
	afterDisk, _ := os.ReadFile(os.Getenv("RADAR_STATE"))
	if afterRevision != revision || !reflect.DeepEqual(records, afterRecords) || !reflect.DeepEqual(refs, afterRefs) || !reflect.DeepEqual(beforeDisk, afterDisk) {
		t.Fatal("preview changed state or publication")
	}
	beforeCalls := provider.calls
	if _, err := Read(store, req, false, providers); err == nil || provider.calls != beforeCalls {
		t.Fatal("cached read invoked native planner")
	}
	req.Cursor = ""
	req.ID = firstID
	exact, err := Probe(context.Background(), store, req, false, providers)
	if err != nil || exact.Total != 1 || exact.Items[0].ID != firstID {
		t.Fatal("exact target selection failed")
	}
	// Same-count private operation drift must invalidate an older page snapshot.
	provider.targets[0].Operation["delete_branch"] = "private-other-branch"
	req.ID = ""
	req.Cursor = firstCursor
	if _, err := Probe(context.Background(), store, req, false, providers); err == nil {
		t.Fatal("private target drift reused cursor")
	}
	provider.targets[0].Operation["delete_branch"] = "private-branch"
	registration.groups = []protocol.DiagnosticWorkspace{{ID: "changed-between-pages"}}
	if _, err := Probe(context.Background(), store, req, false, providers); err == nil {
		t.Fatal("registration drift reused cursor without target changes")
	}
	req.Cursor = ""
	req.Workspace = path + "-unrelated"
	beforeCalls = provider.calls
	if _, err := Probe(context.Background(), store, req, false, providers); err == nil || provider.calls != beforeCalls {
		t.Fatal("unrelated scope reached planner")
	}
}

func TestCleanupPreviewRejectsPendingUnknownUnsafeDriftAndPrivateErrors(t *testing.T) {
	for _, mode := range []string{"pending", "missing-parent", "state", "outside-scope", "unknown-provider", "duplicate-provider", "provider-error", "unknown-effect", "missing-identity", "duplicate-target", "too-many-targets", "too-many-notices", "oversized-target", "aggregate-bytes", "too-many-refs", "cache-drift", "registration-drift", "cancelled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			store, _, path := fixture(t)
			store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
			records, _, _ := store.InspectionSnapshot()
			provider, registration := manualPreviewFixture(t, 1)
			providers := integration.NewRegistry(provider, registration)
			req := protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID, Workspace: path}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pending := false
			switch mode {
			case "pending":
				pending = true
			case "missing-parent":
				req.ParentID = ""
			case "state":
				req.State = "done"
			case "outside-scope":
				req.Workspace = path + "-other"
			case "unknown-provider":
				provider.name = "private-provider"
			case "duplicate-provider":
				providers = integration.NewRegistry(provider, provider, registration)
			case "provider-error":
				provider.err = errors.New("private-token")
			case "unknown-effect":
				provider.targets[0].Kind = "private-kind"
			case "missing-identity":
				provider.targets[0].ResourceID = ""
			case "duplicate-target":
				provider.targets = append(provider.targets, provider.targets[0])
			case "too-many-targets":
				for len(provider.targets) < 513 {
					provider.targets = append(provider.targets, provider.targets[0])
				}
			case "too-many-notices":
				provider.targets[0].Safety = make([]protocol.CleanupSafety, 1025)
			case "oversized-target":
				provider.targets[0].Description = strings.Repeat("private", 10000)
			case "aggregate-bytes":
				provider.targets = nil
				for i := 0; i < 18; i++ {
					provider.targets = append(provider.targets, protocol.CleanupTarget{Source: "git", Kind: "worktree", ResourceID: fmt.Sprint(i), Description: strings.Repeat("private", 8500)})
				}
			case "too-many-refs":
				expanded := task(path, "test:ABC-123")
				for i := 0; i < 257; i++ {
					expanded.SourceRefs = append(expanded.SourceRefs, protocol.SourceRef{ID: fmt.Sprintf("tmux:%d", i), Source: "tmux", Kind: "session"})
				}
				store.SetTasks([]protocol.Task{expanded})
			case "cache-drift":
				provider.run = func(context.Context) { store.NotifyChange() }
			case "registration-drift":
				provider.run = func(context.Context) {
					registration.groups = append(registration.groups, protocol.DiagnosticWorkspace{ID: "changed"})
				}
			case "cancelled":
				cancel()
			case "deadline":
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, time.Millisecond)
				defer deadlineCancel()
				provider.run = func(ctx context.Context) { <-ctx.Done() }
			}
			_, err := Probe(ctx, store, req, pending, providers)
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe/private preview accepted: %v", err)
			}
			switch mode {
			case "pending", "missing-parent", "state", "outside-scope", "unknown-provider", "duplicate-provider", "too-many-refs", "cancelled":
				if provider.calls != 0 {
					t.Fatal("invalid request ran native provider")
				}
			}
		})
	}
}

func TestCleanupTargetEffectsAndNativeSafetyFlags(t *testing.T) {
	for _, tc := range []struct{ source, kind, effect, branch string }{
		{"git", "worktree", "remove_worktree_and_local_changes", "retain_local_branch"},
		{"sbx", "sandbox", "remove_sandbox_and_local_state", ""},
		{"tmux", "session", "remove_terminal_session_and_processes", ""},
		{"workspace", "workspace", "remove_workspace_local_files_registration_and_archive_completed_note", ""},
		{"workspace", "missing_member", "forget_missing_worktree_registration", "retain_local_branch"},
	} {
		v, err := cleanupTargetView(protocol.CleanupTarget{Source: tc.source, Kind: tc.kind, ResourceID: "resource", Safety: []protocol.CleanupSafety{{Kind: "new-private-notice", BlocksAutomatic: true, Expires: true, Message: "private"}}})
		if err != nil || v.Effect != tc.effect || v.BranchEffect != tc.branch || len(v.Safety) != 1 || v.Safety[0].Kind != "other_notice" || !v.Safety[0].BlocksAutomatic || !v.Safety[0].Expires || v.Safety[0].Count != 1 {
			t.Fatalf("effect/safety=%+v %v", v, err)
		}
	}
}
