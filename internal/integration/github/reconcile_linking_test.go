package github

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"radar/internal/integration"
	gitsource "radar/internal/integration/git"
	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestReconciledSameRepositoryWorktreeLinksExistingGitHubPullRequestToWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	if _, err := exec.LookPath("fd"); err != nil {
		t.Skip("fd not found")
	}

	ctx := context.Background()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sources := filepath.Join(tmp, "sources")
	workspaceRoot := filepath.Join(tmp, "workspaces")
	primaryRepo := filepath.Join(sources, "primary")
	anchor := filepath.Join(workspaceRoot, "work")
	primaryPath := filepath.Join(anchor, "primary--work")

	initLinkingRepository(t, ctx, primaryRepo, "https://github.com/acme/primary.git")
	runLinkingGit(t, ctx, primaryRepo, "branch", "feature/api")
	if err := os.MkdirAll(filepath.Dir(primaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runLinkingGit(t, ctx, primaryRepo, "worktree", "add", "-b", "work", primaryPath, "HEAD")

	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "work", Path: anchor,
		Members: []workspacegroup.Member{{Repository: primaryRepo, Path: primaryPath, Branch: "work", SetupScheduled: true}},
	}
	if err := workspacegroup.Save(workspaceRoot, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}

	configHome := filepath.Join(tmp, "config")
	if err := os.MkdirAll(filepath.Join(configHome, "radar"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"linking_mark_prefixes":["XYZ"],"workspace":{"root_dir":"` + workspaceRoot + `"},"repository_dirs":["` + sources + `"]}`
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("RADAR_STATE", filepath.Join(tmp, "state.json"))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	pr := githubPullRequestRef("github:pr:acme/primary:7", "acme/primary", 7, "Feature without ticket", "https://github.com/acme/primary/pull/7", "open PR", "feature/api")
	initialWorktrees, status := gitsource.FetchWorktrees(ctx, logger, linking.NewMarkMatcher([]string{"XYZ"}))
	if status.Status != "ok" || len(initialWorktrees) != 1 {
		t.Fatalf("initial worktrees=%+v status=%+v", initialWorktrees, status)
	}
	store.SetTasks([]protocol.Task{
		taskForLinkingTest(pr),
		taskForLinkingTest(initialWorktrees[0]),
	})
	if tasks := store.Tasks(); len(tasks) != 2 {
		t.Fatalf("tasks before reconciliation=%+v, want separate PR and workspace tasks", tasks)
	}

	inspected, err := workspace.InspectWorkspace(ctx, workspace.ExecRunner{}, primaryPath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	desired := inspected.Desired
	desired.Worktrees = append(desired.Worktrees, workspace.DesiredWorkspaceWorktree{
		Repository: primaryRepo, BranchMode: integration.WorkspaceBranchExisting, Branch: "feature/api",
	})
	request := workspace.ReconcileWorkspaceRequest{
		Workspace: primaryPath, WorkspaceRoot: workspaceRoot, Revision: inspected.Revision, Desired: desired,
	}
	plan, err := workspace.PreviewReconcileWorkspace(ctx, workspace.ExecRunner{}, request)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedPlanID = plan.PlanID
	result, err := workspace.ApplyReconcileWorkspace(ctx, workspace.ExecRunner{}, nil, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.WorktreesAdded != 1 {
		t.Fatalf("reconciliation result=%+v", result)
	}

	refreshedWorktrees, status := gitsource.FetchWorktrees(ctx, logger, linking.NewMarkMatcher([]string{"XYZ"}))
	if status.Status != "ok" || len(refreshedWorktrees) != 2 {
		t.Fatalf("refreshed worktrees=%+v status=%+v", refreshedWorktrees, status)
	}
	localTasks := make([]protocol.Task, 0, len(refreshedWorktrees))
	if refreshedWorktrees[0].ID == refreshedWorktrees[1].ID {
		t.Fatalf("duplicate refreshed worktrees: %+v", refreshedWorktrees)
	}
	for _, ref := range refreshedWorktrees {
		localTasks = append(localTasks, taskForLinkingTest(ref))
	}
	store.SetTasksForSources(localTasks, []string{"git"})

	tasks := store.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("tasks after reconciliation=%+v, want one linked task", tasks)
	}
	for _, id := range []string{"github:pr:acme/primary:7", "git:worktree:" + primaryPath, "git:worktree:" + plan.Changes[0].Path} {
		if !taskHasSourceRef(tasks[0], id) {
			t.Fatalf("linked task is missing %q: %+v", id, tasks[0].SourceRefs)
		}
	}
}

func TestApplyLinkingMarksDoesNotInspectPullRequestBody(t *testing.T) {
	ref := githubPullRequestRef("github:pr:acme/app:7", "acme/app", 7, "Update documentation", "https://github.com/acme/app/pull/7", "open PR", "docs")
	ref.Metadata = map[string]string{"body": "Related to PR XYZ-123"}
	tasks := []protocol.Task{{SourceRefs: []protocol.SourceRef{ref}}}

	applyLinkingMarks(tasks, linking.NewMarkMatcher([]string{"XYZ"}))

	for _, key := range tasks[0].SourceRefs[0].LinkingKeys {
		if key == "mark:XYZ-123" {
			t.Fatalf("PR body unexpectedly contributed linking key: %+v", tasks[0].SourceRefs[0].LinkingKeys)
		}
	}
}

func initLinkingRepository(t *testing.T, ctx context.Context, path, origin string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	runLinkingGit(t, ctx, filepath.Dir(path), "init", filepath.Base(path))
	runLinkingGit(t, ctx, path, "config", "user.name", "Radar Test")
	runLinkingGit(t, ctx, path, "config", "user.email", "radar@example.test")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLinkingGit(t, ctx, path, "add", "README.md")
	runLinkingGit(t, ctx, path, "commit", "-m", "initial")
	runLinkingGit(t, ctx, path, "remote", "add", "origin", origin)
}

func runLinkingGit(t *testing.T, ctx context.Context, directory string, args ...string) {
	t.Helper()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T12:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T12:00:00Z",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func taskForLinkingTest(ref protocol.SourceRef) protocol.Task {
	return protocol.Task{Title: ref.Title, Repo: ref.Repo, Attention: "in_progress", SourceRefs: []protocol.SourceRef{ref}}
}

func taskHasSourceRef(task protocol.Task, id string) bool {
	for _, ref := range task.SourceRefs {
		if ref.ID == id {
			return true
		}
	}
	return false
}
