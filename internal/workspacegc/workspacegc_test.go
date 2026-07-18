package workspacegc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
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
	path := filepath.Join(root, "app", "CAP-7-ship")

	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship")),
		makeTask("in_progress", "git worktree", worktreeRef(path, "acme/app", "CAP-7-ship")),
		makeTask("in_progress", "tmux", detachedSessionRef(path, "radar-app-CAP-7-ship")),
		makeTask("in_progress", "sbx", sandboxRef(path, "radar-app-CAP-7-ship")),
	})

	plan, err := BuildPlan(store, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want one", plan.Candidates)
	}
	candidate := plan.Candidates[0]
	if candidate.Path != path || candidate.SessionName != "radar-app-CAP-7-ship" || candidate.SandboxName != "radar-app-CAP-7-ship" {
		t.Fatalf("candidate = %+v", candidate)
	}
}

func TestBuildPlanSkipsAttachedSession(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	path := filepath.Join(root, "app", "CAP-7-ship")

	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship")),
		makeTask("in_progress", "git worktree", worktreeRef(path, "acme/app", "CAP-7-ship")),
		makeTask("in_progress", "tmux", attachedSessionRef(path, "radar-app-CAP-7-ship")),
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
	path := filepath.Join(root, "app", "CAP-7-ship")

	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship")),
		makeTask("in_progress", "git worktree", worktreeRef(path, "acme/app", "CAP-7-ship")),
	})

	plan, err := BuildPlan(store, time.Now().Add(time.Second), Options{WorkspaceRoot: root, Retention: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none before retention", plan.Candidates)
	}
}

func TestRunSelectsOneWorkspaceBundleAndUsesConservativeExecution(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	firstPath := filepath.Join(root, "app", "CAP-7-first")
	secondPath := filepath.Join(root, "app", "CAP-7-second")
	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "CAP-7-first")),
		makeTask("in_progress", "git worktree", worktreeRef(firstPath, "acme/app", "CAP-7-first")),
		makeTask("in_progress", "tmux", detachedSessionRef(firstPath, "first")),
		makeTask("in_progress", "sbx", sandboxRef(firstPath, "first")),
		makeTask("in_progress", "git worktree", worktreeRef(secondPath, "acme/app", "CAP-7-second")),
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

func TestRunSkipsDirtyWorkspaceWithoutExecutingLinkedTargets(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	path := filepath.Join(root, "app", "CAP-7-ship")
	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship")),
		makeTask("in_progress", "git worktree", worktreeRef(path, "acme/app", "CAP-7-ship")),
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

func TestRunContinuesAfterCandidateFailure(t *testing.T) {
	store := testStore(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	firstPath := filepath.Join(root, "app", "CAP-7-first")
	secondPath := filepath.Join(root, "app", "CAP-7-second")
	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "CAP-7-first")),
		makeTask("in_progress", "git worktree", worktreeRef(firstPath, "acme/app", "CAP-7-first")),
		makeTask("in_progress", "git worktree", worktreeRef(secondPath, "acme/app", "CAP-7-second")),
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
	name     string
	calls    *[]cleanupCall
	dirty    bool
	failPath string
}

func (p gcProvider) Name() string { return p.name }
func (gcProvider) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{}
}
func (p gcProvider) PreviewCleanup(_ context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	targets := []protocol.CleanupTarget{}
	for _, ref := range req.Task.SourceRefs {
		if ref.Source != p.name {
			continue
		}
		target := protocol.CleanupTarget{SourceRefID: ref.ID, Source: ref.Source, Kind: ref.Kind, Path: ref.Path}
		if p.name == "git" {
			target.Dirty = p.dirty
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
	return protocol.SourceRef{ID: id, Source: "github", Kind: "pull_request", Repo: repo, Branch: branch, CanonicalKey: id, LinkingKeys: linking.Keys("ticket:CAP-7", linking.BranchKey(repo, branch))}
}

func worktreeRef(path string, repo string, branch string) protocol.SourceRef {
	return protocol.SourceRef{ID: "git:worktree:" + path, Source: "git", Kind: "worktree", Repo: repo, Path: path, Branch: branch, CanonicalKey: linking.WorkspaceKey(path), LinkingKeys: linking.Keys("ticket:CAP-7", linking.WorkspaceKey(path), linking.BranchKey(repo, branch))}
}

func detachedSessionRef(path string, name string) protocol.SourceRef {
	return protocol.SourceRef{ID: "tmux:session:" + name, Source: "tmux", Kind: "session", Title: name, Path: path, Status: "detached", LinkingKeys: linking.Keys("ticket:CAP-7", linking.WorkspaceKey(path)), Metadata: map[string]string{"session": name, "attached_count": "0"}}
}

func attachedSessionRef(path string, name string) protocol.SourceRef {
	ref := detachedSessionRef(path, name)
	ref.Status = "attached"
	ref.Metadata["attached_count"] = "1"
	return ref
}

func sandboxRef(path string, name string) protocol.SourceRef {
	return protocol.SourceRef{ID: "sbx:sandbox:" + name, Source: "sbx", Kind: "sandbox", Title: name, Path: path, LinkingKeys: linking.Keys("ticket:CAP-7", linking.WorkspaceKey(path)), Metadata: map[string]string{"name": name}}
}
