package workspacegc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestBuildPlanSelectsDoneWorkspaceAfterRetention(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	path := filepath.Join(root, "app", "ABC-7-ship")

	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "ABC-7-ship")),
		makeTask("in_progress", "git worktree", worktreeRef(path, "acme/app", "ABC-7-ship")),
		makeTask("in_progress", "tmux", detachedSessionRef(path, "radar-app-ABC-7-ship")),
		makeTask("in_progress", "sbx", sandboxRef(path, "radar-app-ABC-7-ship")),
	})

	plan, err := BuildPlan(store, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want one", plan.Candidates)
	}
	candidate := plan.Candidates[0]
	if candidate.Path != path {
		t.Fatalf("candidate = %+v", candidate)
	}
}

func TestRunGarbageCollectsNoteOnlyWorkspace(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	anchor := filepath.Join(root, "plan")
	key := "obsidian:task:12345678-1234-4123-8123-123456789abc"
	workItem := protocol.SourceRef{
		ID: key, EntityID: key, Source: "obsidian", Kind: "task", Role: protocol.SourceRefRoleAuthoritative,
		Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary,
		CanonicalKey: key, LinkingKeys: []string{key}, Signal: "done", Title: "Plan",
	}
	workspaceID := "note-workspace"
	workspaceRef := protocol.SourceRef{
		ID: "workspace:" + workspaceID, EntityID: "workspace:" + workspaceID, Source: "workspace", Kind: "workspace",
		Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace, Authority: protocol.SourceRefAuthorityNone,
		Path: anchor, ProvidesWorkspace: true, WorkspaceEntry: true, WorkspaceID: workspaceID,
		CanonicalKey: linking.WorkspaceKey(anchor), LinkingKeys: linking.Keys(key, linking.WorkspaceGroupKey(workspaceID)),
		Metadata: map[string]string{"workspace_id": workspaceID},
	}
	store.SetTasks([]protocol.Task{makeTask("done", "completed", workItem), makeTask("in_progress", "workspace", workspaceRef)})
	calls := []cleanupCall{}
	provider := gcProvider{name: "workspace", calls: &calls}
	result, err := Run(context.Background(), store, cleanup.New([]integration.CleanupProvider{provider}), nil, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 1 || len(calls) != 1 || calls[0].path != anchor {
		t.Fatalf("result = %+v, calls = %+v", result, calls)
	}
}

func TestBuildPlanSkipsAttachedSession(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	path := filepath.Join(root, "app", "ABC-7-ship")

	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "ABC-7-ship")),
		makeTask("in_progress", "git worktree", worktreeRef(path, "acme/app", "ABC-7-ship")),
		makeTask("in_progress", "tmux", attachedSessionRef(path, "radar-app-ABC-7-ship")),
	})

	plan, err := BuildPlan(store, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("plan = %+v, want one skipped attached session", plan)
	}
}

func TestBuildPlanWaitsForRetention(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	path := filepath.Join(root, "app", "ABC-7-ship")

	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "ABC-7-ship")),
		makeTask("in_progress", "git worktree", worktreeRef(path, "acme/app", "ABC-7-ship")),
	})

	plan, err := BuildPlan(store, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none before retention", plan.Candidates)
	}
}

func TestBuildPlanIncludesNewlyDoneWorkspaceWhenRetentionIsIgnored(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	path := filepath.Join(root, "app", "ABC-7-ship")

	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "ABC-7-ship")),
		makeTask("in_progress", "git worktree", worktreeRef(path, "acme/app", "ABC-7-ship")),
		makeTask("in_progress", "sbx", sandboxRef(path, "radar-app-ABC-7-ship")),
	})

	plan, err := BuildPlan(store, time.Now(), Options{WorkspaceRoot: root, Retention: 48 * time.Hour, IgnoreRetention: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want newly done workspace bundle", plan.Candidates)
	}
}

func TestRunSelectsOneWorkspaceBundleAndUsesConservativeExecution(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	firstPath := filepath.Join(root, "app", "ABC-7-first")
	secondPath := filepath.Join(root, "app", "ABC-7-second")
	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "ABC-7-first")),
		makeTask("in_progress", "git worktree", worktreeRef(firstPath, "acme/app", "ABC-7-first")),
		makeTask("in_progress", "tmux", detachedSessionRef(firstPath, "first")),
		makeTask("in_progress", "sbx", sandboxRef(firstPath, "first")),
		makeTask("in_progress", "git worktree", worktreeRef(secondPath, "acme/app", "ABC-7-second")),
		makeTask("in_progress", "tmux", detachedSessionRef(secondPath, "second")),
	})

	calls := []cleanupCall{}
	providers := []integration.CleanupProvider{
		gcProvider{name: "tmux", calls: &calls},
		gcProvider{name: "sbx", calls: &calls},
		gcProvider{name: "git", calls: &calls},
	}
	result, err := Run(context.Background(), store, cleanup.New(providers), nil, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 2 || len(result.Skipped) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(calls) != 5 {
		t.Fatalf("calls = %+v, want two independent bundles", calls)
	}
	for _, call := range calls {
		if call.force {
			t.Fatalf("cleanup call used force: %+v", call)
		}
	}
	for i := 0; i < 3; i++ {
		if calls[i].path != firstPath {
			t.Fatalf("first bundle calls = %+v", calls[:3])
		}
	}
	for i := 3; i < 5; i++ {
		if calls[i].path != secondPath {
			t.Fatalf("second bundle calls = %+v", calls[3:])
		}
	}
}

func TestRunTreatsRegisteredMembersAsOneConservativeBundle(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	anchor := filepath.Join(root, "ABC-7-ship")
	primary := filepath.Join(anchor, "app--ABC-7-ship")
	secondary := filepath.Join(anchor, "api--ABC-7-api")
	id := "registered-workspace"
	primaryRef := worktreeRef(primary, "acme/app", "ABC-7-ship")
	primaryRef.Metadata = map[string]string{"workspace_id": id}
	primaryRef.WorkspaceID = id
	primaryRef.ProvidesWorkspace = false
	secondaryRef := worktreeRef(secondary, "acme/api", "ABC-7-api")
	secondaryRef.Metadata = map[string]string{"workspace_id": id}
	secondaryRef.WorkspaceID = id
	secondaryRef.ProvidesWorkspace = false
	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "ABC-7-ship")),
		makeTask("in_progress", "workspace", workspaceAnchorRef(anchor, id)),
		makeTask("in_progress", "primary", primaryRef),
		makeTask("in_progress", "secondary", secondaryRef),
		makeTask("in_progress", "tmux", detachedSessionRef(anchor, "session")),
	})
	plan, err := BuildPlan(store, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].WorkspaceID != id {
		t.Fatalf("plan = %+v", plan)
	}

	calls := []cleanupCall{}
	providers := []integration.CleanupProvider{gcProvider{name: "tmux", calls: &calls}, gcProvider{name: "git", calls: &calls, dirtyPath: secondary}}
	result, err := Run(context.Background(), store, cleanup.New(providers), nil, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 0 || len(result.Skipped) != 1 || len(calls) != 0 {
		t.Fatalf("result = %+v, calls = %+v", result, calls)
	}
}

func TestRunSkipsDirtyWorkspaceWithoutExecutingLinkedTargets(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	path := filepath.Join(root, "app", "ABC-7-ship")
	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "ABC-7-ship")),
		makeTask("in_progress", "git worktree", worktreeRef(path, "acme/app", "ABC-7-ship")),
		makeTask("in_progress", "tmux", detachedSessionRef(path, "session")),
	})

	calls := []cleanupCall{}
	providers := []integration.CleanupProvider{
		gcProvider{name: "tmux", calls: &calls},
		gcProvider{name: "git", calls: &calls, dirty: true},
	}
	result, err := Run(context.Background(), store, cleanup.New(providers), nil, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 0 || len(result.Skipped) != 1 || len(calls) != 0 {
		t.Fatalf("result = %+v, calls = %+v", result, calls)
	}
}

func TestRunSkipsManagedBranchThatMayContainLocalOnlyCommits(t *testing.T) {
	for _, test := range []struct {
		name               string
		unpublished        bool
		publicationUnknown bool
		wantReason         string
	}{
		{name: "unpublished", unpublished: true, wantReason: "not found on a remote-tracking branch"},
		{name: "unknown", publicationUnknown: true, wantReason: "could not be verified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			root := filepath.Join(t.TempDir(), "workspaces")
			path := filepath.Join(root, "app", "ABC-7-ship")
			store.SetTasks([]protocol.Task{
				makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "ABC-7-ship")),
				makeTask("in_progress", "git worktree", worktreeRef(path, "acme/app", "ABC-7-ship")),
			})
			calls := []cleanupCall{}
			provider := gcProvider{name: "git", calls: &calls, deleteBranch: true, unpublished: test.unpublished, publicationUnknown: test.publicationUnknown}
			result, err := Run(context.Background(), store, cleanup.New([]integration.CleanupProvider{provider}), nil, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: time.Nanosecond})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Deleted) != 0 || len(result.Skipped) != 1 || len(calls) != 0 || !strings.Contains(result.Skipped[0].Reason, test.wantReason) {
				t.Fatalf("result = %+v, calls = %+v", result, calls)
			}
		})
	}
}

func TestRunContinuesAfterCandidateFailure(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	firstPath := filepath.Join(root, "app", "ABC-7-first")
	secondPath := filepath.Join(root, "app", "ABC-7-second")
	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "ABC-7-first")),
		makeTask("in_progress", "git worktree", worktreeRef(firstPath, "acme/app", "ABC-7-first")),
		makeTask("in_progress", "git worktree", worktreeRef(secondPath, "acme/app", "ABC-7-second")),
	})

	calls := []cleanupCall{}
	provider := gcProvider{name: "git", calls: &calls, failPath: firstPath}
	result, err := Run(context.Background(), store, cleanup.New([]integration.CleanupProvider{provider}), nil, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].Path != secondPath || len(result.Skipped) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

type cleanupCall struct {
	source string
	path   string
	force  bool
}

type gcProvider struct {
	name               string
	calls              *[]cleanupCall
	dirty              bool
	dirtyPath          string
	deleteBranch       bool
	unpublished        bool
	publicationUnknown bool
	failPath           string
}

func (p gcProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: p.name}
}
func (gcProvider) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{}
}
func (p gcProvider) PreviewCleanup(_ context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	targets := []protocol.CleanupTarget{}
	for _, ref := range req.Task.SourceRefs {
		if ref.Source != p.name {
			continue
		}
		target := protocol.CleanupTarget{
			SourceRefID: ref.ID, Source: ref.Source, Kind: ref.Kind, Path: ref.Path,
			ProvidesWorkspace: ref.ProvidesWorkspace, WorkspaceID: ref.WorkspaceID,
		}
		if p.name == "git" {
			if p.dirty || (p.dirtyPath != "" && ref.Path == p.dirtyPath) {
				target.Safety = append(target.Safety, protocol.CleanupSafety{Kind: "local_changes", Message: "workspace has local changes", BlocksAutomatic: true})
			}
			if p.deleteBranch {
				target.Operation = map[string]string{"delete_branch": ref.Branch}
			}
			if p.unpublished {
				target.Safety = append(target.Safety, protocol.CleanupSafety{Kind: "unpublished_data", Message: "branch has commits not found on a remote-tracking branch", BlocksAutomatic: true})
			}
			if p.publicationUnknown {
				target.Safety = append(target.Safety, protocol.CleanupSafety{Kind: "safety_check_unavailable", Message: "branch publication could not be verified", BlocksAutomatic: true})
			}
		}
		targets = append(targets, target)
	}
	return targets, nil
}
func (p gcProvider) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	*p.calls = append(*p.calls, cleanupCall{source: p.name, path: req.Target.Path, force: req.Force})
	if req.Target.Path == p.failPath {
		return protocol.CleanupTarget{}, errors.New("failed")
	}
	return req.Target, nil
}

func testStore(t *testing.T) *state.Store {
	t.Helper()
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func makeTask(attention string, reason string, ref protocol.SourceRef) protocol.Task {
	if ref.Signal == "" {
		ref.Signal = attention
	}
	return protocol.Task{Title: ref.Title, Attention: attention, Reason: reason, SourceRefs: []protocol.SourceRef{ref}}
}

func githubRef(id string, repo string, branch string) protocol.SourceRef {
	return protocol.SourceRef{ID: id, EntityID: id, Source: "github", Kind: "pull_request", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Repo: repo, Branch: branch, CanonicalKey: id, LinkingKeys: linking.Keys("mark:ABC-7", linking.BranchKey(repo, branch))}
}

func worktreeRef(path string, repo string, branch string) protocol.SourceRef {
	return protocol.SourceRef{ID: "git:worktree:" + path, EntityID: "git:worktree:" + path, Source: "git", Kind: "worktree", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace, Repo: repo, Path: path, ProvidesWorkspace: true, Branch: branch, CanonicalKey: linking.WorkspaceKey(path), LinkingKeys: linking.Keys("mark:ABC-7", linking.WorkspaceKey(path), linking.BranchKey(repo, branch))}
}

func workspaceAnchorRef(path string, id string) protocol.SourceRef {
	return protocol.SourceRef{
		ID: "workspace:" + id, EntityID: "workspace:" + id, Source: "workspace", Kind: "workspace",
		Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace,
		Path: path, ProvidesWorkspace: true, WorkspaceEntry: true, WorkspaceID: id,
		CanonicalKey: linking.WorkspaceKey(path), LinkingKeys: linking.Keys("mark:ABC-7", linking.WorkspaceKey(path), linking.WorkspaceGroupKey(id)),
	}
}

func detachedSessionRef(path string, name string) protocol.SourceRef {
	return protocol.SourceRef{ID: "tmux:session:" + name, EntityID: "tmux:session:" + name, Source: "tmux", Kind: "session", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource, Title: name, Path: path, Status: "detached", LinkingKeys: linking.Keys("mark:ABC-7", linking.WorkspaceKey(path)), Metadata: map[string]string{"session": name, "attached_count": "0"}}
}

func attachedSessionRef(path string, name string) protocol.SourceRef {
	ref := detachedSessionRef(path, name)
	ref.Status = "attached"
	ref.InUse = true
	ref.Metadata["attached_count"] = "1"
	return ref
}

func sandboxRef(path string, name string) protocol.SourceRef {
	return protocol.SourceRef{ID: "sbx:sandbox:" + name, EntityID: "sbx:sandbox:" + name, Source: "sbx", Kind: "sandbox", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource, Title: name, Path: path, LinkingKeys: linking.Keys("mark:ABC-7", linking.WorkspaceKey(path)), Metadata: map[string]string{"name": name}}
}
