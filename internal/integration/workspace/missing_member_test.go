package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestMissingMemberCleanupForgetsOnlyRegistrationAndKeepsBranch(t *testing.T) {
	root := configureWorkspaceRoot(t)
	ctx := context.Background()
	repo := filepath.Join(filepath.Dir(root), "repo")
	runGitE2E(t, ctx, filepath.Dir(root), "init", repo)
	runGitE2E(t, ctx, repo, "config", "user.email", "radar@example.test")
	runGitE2E(t, ctx, repo, "config", "user.name", "Radar Test")
	runGitE2E(t, ctx, repo, "commit", "--allow-empty", "-m", "initial")
	anchor := filepath.Join(root, "feature")
	path := filepath.Join(anchor, "repo--feature")
	runGitE2E(t, ctx, repo, "worktree", "add", "-b", "feature", path)
	runGitE2E(t, ctx, path, "commit", "--allow-empty", "-m", "unpublished commit kept on branch")
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "feature", Path: anchor, Members: []workspacegroup.Member{{Repository: repo, Path: path, Branch: "feature"}}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "worktree", "lock", path)
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	ref := protocol.SourceRef{ID: "workspace:" + group.ID, Source: "workspace", Kind: "workspace", Path: anchor}
	collected := (Source{}).Collect(ctx, integration.CollectRequest{})
	if len(collected.Observations) != 1 || len(collected.Observations[0].Ref.CleanupIssues) != 1 || !strings.Contains(collected.Observations[0].Ref.CleanupIssues[0], "locked") {
		t.Fatalf("missing locked issue: %+v", collected)
	}
	if _, err := (Source{}).PreviewCleanup(ctx, integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{ref}}}); err == nil {
		t.Fatal("locked missing member was allowed")
	}
	runGitE2E(t, ctx, repo, "worktree", "unlock", path)
	targets, err := (Source{}).PreviewCleanup(ctx, integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{ref}}})
	if err != nil || len(targets) != 2 || targets[0].Kind != "missing_member" {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	// Reappearing user content must be protected, even after a successful preview.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Source{}).Cleanup(ctx, integration.CleanupRequest{Target: targets[0]}); err == nil {
		t.Fatal("reappearing directory was forgotten")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if _, err := (Source{}).Cleanup(ctx, integration.CleanupRequest{Target: target}); err != nil {
			t.Fatal(err)
		}
	}
	runGitE2E(t, ctx, repo, "show-ref", "--verify", "refs/heads/feature")
	list, err := (ExecRunner{}).Run(ctx, repo, "git", "worktree", "list", "--porcelain")
	if err != nil || strings.Contains(list, "branch refs/heads/feature") {
		t.Fatalf("stale registration remains: %s %v", list, err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatalf("registry=%+v err=%v", registry, err)
	}
}
